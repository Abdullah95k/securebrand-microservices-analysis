package media_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	analysisv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/analysis/v1"
	mediav1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/media/v1"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/media"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/pipeline"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider/fake"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider/registry"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/taxonomy"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/testenv"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/textnorm"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/topics"
	fbv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/fb/v1"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		img.Set(x, x%h, color.RGBA{255, 0, 0, 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestResize(t *testing.T) {
	out, err := media.Resize(pngBytes(t, 3000, 1500), 1024, 0)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 1024 || b.Dy() != 512 {
		t.Fatalf("resized to %v", b)
	}
	small, _ := media.Resize(pngBytes(t, 300, 600), 1024, 0)
	img, _ = jpeg.Decode(bytes.NewReader(small))
	if b := img.Bounds(); b.Dx() != 300 || b.Dy() != 600 {
		t.Fatalf("small image resized to %v", b)
	}
	tall, _ := media.Resize(pngBytes(t, 500, 2000), 1000, 0)
	img, _ = jpeg.Decode(bytes.NewReader(tall))
	if b := img.Bounds(); b.Dx() != 250 || b.Dy() != 1000 {
		t.Fatalf("tall image resized to %v", b)
	}
	if _, err := media.Resize([]byte("not an image"), 100, 0); err == nil {
		t.Fatal("garbage decoded")
	}
}

// testVideo generates a short clip with sound, or skips without ffmpeg.
func testVideo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	p := filepath.Join(t.TempDir(), "clip.mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=duration=6:size=640x360:rate=10",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=6", "-shortest", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", p)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot generate a test video: %v %s", err, out)
	}
	return p
}

func TestExtractFrames(t *testing.T) {
	video := testVideo(t)
	f, err := media.ExtractFrames(context.Background(), "ffmpeg", video, t.TempDir(), 6000, 4, 320, 60)
	if err != nil {
		t.Fatal(err)
	}
	dir := f.Dir
	if len(f.Images) < 3 || len(f.Images) > 4 || len(f.Audio) == 0 {
		t.Fatalf("%d frames, %d audio bytes", len(f.Images), len(f.Audio))
	}
	img, err := jpeg.Decode(bytes.NewReader(f.Images[0]))
	if err != nil || img.Bounds().Dx() != 320 {
		t.Fatalf("frame %v %v", img.Bounds(), err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("temp dir not removed")
	}
	if _, err := media.ExtractFrames(context.Background(), "no-such-ffmpeg", video, "", 0, 4, 320, 60); !errors.Is(err, media.ErrNoFFmpeg) {
		t.Fatalf("missing ffmpeg: %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.mp4")
	_ = os.WriteFile(bad, []byte("nope"), 0o600)
	if _, err := media.ExtractFrames(context.Background(), "ffmpeg", bad, "", 0, 4, 320, 60); err == nil {
		t.Fatal("invalid video accepted")
	}
}

type objects map[string][]byte

func (o objects) Fetch(_ context.Context, bucket, key string, max int64, dst io.Writer) (int64, error) {
	b, ok := o[bucket+"/"+key]
	if !ok {
		return 0, media.ErrGone
	}
	return media.CopyLimited(dst, bytes.NewReader(b), max)
}

func setup(t *testing.T) (*store.Store, *media.Worker, *fake.Provider, objects) {
	t.Helper()
	return setupWith(t, nil)
}

func setupWith(t *testing.T, edit func(*config.Analysis)) (*store.Store, *media.Worker, *fake.Provider, objects) {
	t.Helper()
	db := testenv.Postgres(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	full, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg := full.Analysis
	for name, r := range cfg.Routes {
		r.Provider = "fake"
		cfg.Routes[name] = r
	}
	cfg.Media.Video.STTProvider = "fake"
	cfg.Media.Video.MaxFrames = 3
	// Most tests announce files without storing their post; waiting for
	// the post (owner_wait) has its own tests, which set it back.
	cfg.Media.OwnerWait = 0
	if edit != nil {
		edit(&cfg)
	}
	loc, _ := time.LoadLocation(cfg.Timezone)
	st := store.New(db, loc)
	tax, _ := taxonomy.Load("")
	reg, _ := registry.New(nil)
	fp := fake.New("fake")
	reg.Add("fake", fp, false, 0)
	p := pipeline.New(st, reg, tax, cfg, nil)
	objs := objects{}
	return st, &media.Worker{P: p, Objects: objs, Cfg: cfg.Media}, fp, objs
}

func job(id, kind string) *core.MediaJob {
	return &core.MediaJob{MediaID: id, OwnerContentID: "fb:post:1", Platform: "facebook", SourceID: "fb:page:1", Kind: kind,
		Bucket: "b", Key: id, DeleteAfter: time.Now().Add(72 * time.Hour), AckFormat: "fb", DurationMS: 6000}
}

func acks(t *testing.T, st *store.Store) map[string]fbv1.ProcessStatus {
	t.Helper()
	rows, err := st.DB.Query(context.Background(), `SELECT payload FROM analysis.outbox WHERE topic = $1`, topics.MediaProcessd)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]fbv1.ProcessStatus{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			t.Fatal(err)
		}
		var m fbv1.MediaProcessed
		if err := proto.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		if m.Consumer != "analysis" {
			t.Fatalf("ack consumer %q", m.Consumer)
		}
		out[m.MediaId] = m.Status
	}
	return out
}

func TestWorkerAnalysesSkipsAndFails(t *testing.T) {
	st, w, fp, objs := setup(t)
	ctx := context.Background()
	objs["b/img"] = pngBytes(t, 2000, 1000)
	objs["b/bad"] = []byte("garbage")
	objs["b/canon"] = pngBytes(t, 200, 200)
	canon := job("canon", "image") // announced through the contract (content.media.ready.v1)
	canon.MediaID, canon.Key, canon.OwnerContentID, canon.SourceID = "fb:media:canon", "canon", "fb:post:1", "fb:account:1"
	canon.AckFormat, canon.Role = core.AckContract, "content"
	jobs := []*core.MediaJob{job("img", "photo"), job("stk", "sticker"), job("bad", "photo"), job("gone", "photo"), canon}
	if _, err := st.InsertMediaJobs(ctx, jobs, "analysis"); err != nil {
		t.Fatal(err)
	}
	var seen []int
	fp.Fail = nil
	for i := 0; i < 5; i++ {
		n, err := w.Once(ctx)
		if err != nil {
			t.Fatal(err)
		}
		seen = append(seen, n)
	}
	got := acks(t, st)
	want := map[string]fbv1.ProcessStatus{
		"img":  fbv1.ProcessStatus_PROCESS_STATUS_PROCESSED,
		"stk":  fbv1.ProcessStatus_PROCESS_STATUS_PROCESSED, // policy: acknowledged without analysis
		"bad":  fbv1.ProcessStatus_PROCESS_STATUS_FAILED_PERMANENT,
		"gone": fbv1.ProcessStatus_PROCESS_STATUS_FAILED_PERMANENT,
	}
	for id, s := range want {
		if got[id] != s {
			t.Errorf("%s acked %v, want %v (all %v, rounds %v)", id, got[id], s, got, seen)
		}
	}
	var n int
	_ = st.DB.QueryRow(ctx, `SELECT count(*) FROM analysis.media_analyses WHERE media_id IN ('img', 'fb:media:canon') AND description <> ''`).Scan(&n)
	if n != 2 {
		t.Fatal("image analyses not stored (Facebook photo and contract image)")
	}
	// The contract's image is published on analysis.media.v1; fbs's photo
	// is only acknowledged on fbs's topic.
	_ = st.DB.QueryRow(ctx, `SELECT count(*) FROM analysis.outbox WHERE topic = 'analysis.media.v1' AND key = 'fb:media:canon'`).Scan(&n)
	if n != 1 {
		t.Fatal("contract image not published on analysis.media.v1")
	}
	_ = st.DB.QueryRow(ctx, `SELECT count(*) FROM analysis.outbox WHERE topic = 'analysis.media.v1' AND key = 'img'`).Scan(&n)
	if n != 0 {
		t.Fatal("fbs's photo published on the contract's analysis.media.v1")
	}
	_ = st.DB.QueryRow(ctx, `SELECT count(*) FROM analysis.provider_requests WHERE route = 'image' AND status = 'done'`).Scan(&n)
	if n != 2 {
		t.Fatal("image request not recorded for the budget")
	}
	// A re-announcement of a done job changes nothing; of a failed one, retries.
	if k, _ := st.InsertMediaJobs(ctx, []*core.MediaJob{job("img", "photo"), job("bad", "photo")}, "analysis"); k != 1 {
		t.Fatalf("re-announcement reopened %d jobs, want 1 (the failed one)", k)
	}
}

func TestWorkerRetriesTransientErrors(t *testing.T) {
	st, w, fp, objs := setup(t)
	ctx := context.Background()
	objs["b/img"] = pngBytes(t, 100, 100)
	fp.Fail = func(provider.Request) error { return &provider.Error{Retryable: true, Status: 503} }
	if _, err := st.InsertMediaJobs(ctx, []*core.MediaJob{job("img", "photo")}, "analysis"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if len(acks(t, st)) != 0 {
		t.Fatal("transient error acknowledged")
	}
	var state string
	var tries int
	_ = st.DB.QueryRow(ctx, `SELECT state, tries FROM analysis.media_jobs WHERE media_id = 'img'`).Scan(&state, &tries)
	if state != "pending" || tries != 1 {
		t.Fatalf("state %s tries %d", state, tries)
	}
}

func TestWorkerVideo(t *testing.T) {
	video := testVideo(t)
	st, w, fp, objs := setup(t)
	ctx := context.Background()
	b, err := os.ReadFile(video)
	if err != nil {
		t.Fatal(err)
	}
	objs["b/vid"] = b
	if _, err := st.InsertMediaJobs(ctx, []*core.MediaJob{job("vid", "video")}, "analysis"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if acks(t, st)["vid"] != fbv1.ProcessStatus_PROCESS_STATUS_PROCESSED {
		t.Fatal("video not acknowledged")
	}
	var transcript string
	if err := st.DB.QueryRow(ctx, `SELECT coalesce(transcript, '') FROM analysis.media_analyses WHERE media_id = 'vid'`).Scan(&transcript); err != nil || transcript == "" {
		t.Fatalf("transcript %q %v", transcript, err)
	}
	if fp.Calls() != 1 {
		t.Fatalf("vision calls %d", fp.Calls())
	}
}

// Speech-to-text is paid per minute: its cost is logged and counts toward
// the daily budget, which is checked before transcribing.
func TestWorkerVideoSpeechCost(t *testing.T) {
	video := testVideo(t)
	b, err := os.ReadFile(video)
	if err != nil {
		t.Fatal(err)
	}
	price := func(c *config.Analysis) {
		pv := c.Providers["fake"]
		pv.TranscribeModel = "stt-test"
		c.Providers["fake"] = pv
		c.Pricing["stt-test"] = config.ModelPrice{PerMinute: 60} // $1 a second
	}
	st, w, _, objs := setupWith(t, price)
	ctx := context.Background()
	objs["b/vid"] = b
	if _, err := st.InsertMediaJobs(ctx, []*core.MediaJob{job("vid", "video")}, "analysis"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	var cost float64
	if err := st.DB.QueryRow(ctx, `SELECT coalesce(sum(cost_usd), 0)::float8 FROM analysis.provider_requests WHERE model = 'stt-test' AND route = 'video'`).Scan(&cost); err != nil {
		t.Fatal(err)
	}
	if cost < 1 {
		t.Fatalf("speech cost %v, want the audio's seconds in dollars", cost)
	}
	var source string
	if err := st.DB.QueryRow(ctx, `SELECT result->>'transcript_source' FROM analysis.media_analyses WHERE media_id = 'vid'`).Scan(&source); err != nil ||
		source != "stt-test" {
		t.Fatalf("transcript source %q %v, want the speech-to-text model", source, err)
	}

	// A budget smaller than the speech cost: nothing is transcribed or sent.
	st2, w2, fp2, objs2 := setupWith(t, func(c *config.Analysis) {
		price(c)
		c.Budget.DailyUSD = 0.5
	})
	objs2["b/vid"] = b
	if _, err := st2.InsertMediaJobs(ctx, []*core.MediaJob{job("vid", "video")}, "analysis"); err != nil {
		t.Fatal(err)
	}
	if _, err := w2.Once(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st2.DB.QueryRow(ctx, `SELECT count(*) FROM analysis.provider_requests`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 || fp2.Calls() != 0 {
		t.Fatalf("over budget: %d requests logged, %d calls", n, fp2.Calls())
	}
}

func TestAudioMinutes(t *testing.T) {
	if got := media.AudioMinutes(make([]byte, 240000)); got != 1 {
		t.Fatalf("240 kB of 32 kbit/s audio = %v minutes, want 1", got)
	}
}

// The owner's text reaches the vision model with the people it tags
// written as "@user"; the stored text keeps them.
func TestWorkerHidesTaggedPeople(t *testing.T) {
	st, w, fp, objs := setup(t)
	ctx := context.Background()
	text := "صورة اليوم مع Sara Ahmed"
	i := strings.Index(text, "Sara Ahmed")
	owner := &core.Item{ContentID: "fb:post:1", Platform: "facebook", Kind: core.KindPost, SourceID: "fb:page:1",
		CreatedAt: time.Now().UTC().Add(-time.Hour), ObservedAt: time.Now().UTC(), Text: text, Status: core.StatusVisible,
		RemoveSpans: []textnorm.Span{{Start: i, End: i + len("Sara Ahmed")}}}
	if _, err := st.Ingest(ctx, []*core.Item{owner}, w.P.Plan); err != nil {
		t.Fatal(err)
	}
	objs["b/img"] = pngBytes(t, 300, 200)
	if _, err := st.InsertMediaJobs(ctx, []*core.MediaJob{job("img", "photo")}, "analysis"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var users []string
	fp.Fail = func(req provider.Request) error {
		mu.Lock()
		users = append(users, req.User)
		mu.Unlock()
		return nil
	}
	if _, err := w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || strings.Contains(users[0], "Sara") || !strings.Contains(users[0], textnorm.TagPlaceholder) {
		t.Fatalf("vision request %q", users)
	}
	var stored string
	if err := st.DB.QueryRow(ctx, `SELECT text FROM analysis.content_items WHERE content_id = 'fb:post:1'`).Scan(&stored); err != nil || stored != text {
		t.Fatalf("stored text %q %v", stored, err)
	}
}

func contractJob(id, role, sha string, deleteIn time.Duration) *core.MediaJob {
	return &core.MediaJob{MediaID: id, OwnerContentID: "tt:post:1", Platform: "tiktok", SourceID: "tt:account:1", Kind: "image",
		Role: role, Bucket: "b", Key: id, Sha256: sha, DeleteAfter: time.Now().Add(deleteIn), AckFormat: core.AckContract, Attempt: 1}
}

// contractAcks reads the acknowledgements on the contract's
// content.media.processed.v1, by media ID.
func contractAcks(t *testing.T, st *store.Store) map[string][]*mediav1.MediaProcessed {
	t.Helper()
	rows, err := st.DB.Query(context.Background(), `SELECT payload FROM analysis.outbox WHERE topic = $1 ORDER BY id`, topics.ContentMediaProcessed)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string][]*mediav1.MediaProcessed{}
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			t.Fatal(err)
		}
		var m mediav1.MediaProcessed
		if err := proto.Unmarshal(b, &m); err != nil {
			t.Fatal(err)
		}
		out[m.MediaId] = append(out[m.MediaId], &m)
	}
	return out
}

// TestContractMediaHandOff covers the contract's media hand-off: analysed,
// reused for an identical file, skipped by policy, failed, and deleted
// before analysis; every acknowledgement as sb-analysis, with a reason on
// skips and failures and an event ID of its own.
func TestContractMediaHandOff(t *testing.T) {
	st, w, fp, objs := setup(t)
	ctx := context.Background()
	sha := strings.Repeat("ab", 32)
	objs["b/tt:media:a"] = pngBytes(t, 300, 200)
	objs["b/tt:media:x"] = []byte("garbage")
	// tt:media:b is the same file as tt:media:a under another ID; its
	// object is not in storage, so it can only be served by reuse.
	sticker := contractJob("tt:media:s", "attachment", "", 3*time.Hour)
	sticker.Kind = "sticker"
	jobs := []*core.MediaJob{contractJob("tt:media:a", "content", sha, time.Hour), contractJob("tt:media:b", "content", sha, 2*time.Hour),
		sticker, contractJob("tt:media:x", "content", "", 4*time.Hour), contractJob("tt:media:g", "content", "", 5*time.Hour)}
	if _, err := st.InsertMediaJobs(ctx, jobs, "analysis"); err != nil {
		t.Fatal(err)
	}
	// The platform deleted tt:media:g's file before it was analysed.
	if n, err := st.MediaGone(ctx, []*core.MediaGone{{MediaID: "tt:media:g", Reason: "expired"}, {MediaID: "tt:media:none"}}); err != nil || n != 1 {
		t.Fatalf("gone: %d %v", n, err)
	}
	for i := 0; i < 6; i++ {
		if _, err := w.Once(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got := contractAcks(t, st)
	want := map[string]struct {
		status mediav1.ProcessStatus
		reason string
	}{
		"tt:media:a": {mediav1.ProcessStatus_PROCESS_STATUS_PROCESSED, ""},
		"tt:media:b": {mediav1.ProcessStatus_PROCESS_STATUS_PROCESSED, ""},
		"tt:media:s": {mediav1.ProcessStatus_PROCESS_STATUS_SKIPPED, "policy:sticker/attachment"},
		"tt:media:x": {mediav1.ProcessStatus_PROCESS_STATUS_FAILED_PERMANENT, "undecodable"},
	}
	ids := map[string]bool{}
	for id, wa := range want {
		acks := got[id]
		if len(acks) != 1 {
			t.Fatalf("%s: %d acknowledgements", id, len(acks))
		}
		a := acks[0]
		if a.Status != wa.status || a.Reason != wa.reason || a.Consumer != "sb-analysis" || a.Platform != "tt" ||
			a.EventId == "" || ids[a.EventId] || a.ProcessedAt == nil {
			t.Errorf("%s: %v", id, a)
		}
		ids[a.EventId] = true
	}
	if a := got["tt:media:x"]; len(a) == 1 && a[0].Error == "" {
		t.Error("a failure's acknowledgement has no error detail")
	}
	if len(got["tt:media:g"]) != 0 {
		t.Error("a deleted file was acknowledged")
	}
	var state string
	if err := st.DB.QueryRow(ctx, `SELECT state FROM analysis.media_jobs WHERE media_id = 'tt:media:g'`).Scan(&state); err != nil || state != "gone" {
		t.Fatalf("deleted file's job %q %v", state, err)
	}
	// One provider call: tt:media:b reused tt:media:a's analysis.
	if n := fp.Calls(); n != 1 {
		t.Fatalf("%d provider calls, want 1", n)
	}
	var from, desc string
	if err := st.DB.QueryRow(ctx, `SELECT coalesce(b.reused_from, ''), b.description FROM analysis.media_analyses b
		JOIN analysis.media_analyses a ON a.media_id = 'tt:media:a' AND a.description = b.description AND a.sha256 = b.sha256
		WHERE b.media_id = 'tt:media:b'`).Scan(&from, &desc); err != nil || from != "tt:media:a" || desc == "" {
		t.Fatalf("reused analysis: from %q %v", from, err)
	}
	// Both analyses are published on analysis.media.v1; nothing for the
	// skipped, failed or deleted ones.
	var keys []string
	rows, err := st.DB.Query(ctx, `SELECT key FROM analysis.outbox WHERE topic = 'analysis.media.v1' ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		keys = append(keys, k)
	}
	rows.Close()
	if strings.Join(keys, ",") != "tt:media:a,tt:media:b" {
		t.Fatalf("published media analyses %v", keys)
	}
	// Nothing went to fbs's acknowledgement topic.
	if a := acks(t, st); len(a) != 0 {
		t.Fatalf("contract media acknowledged on fbs's topic: %v", a)
	}
}

// A post's caption track is its video's transcript: no speech-to-text is
// bought, and the captions leave masked like any transcript.
func TestWorkerVideoUsesCaptions(t *testing.T) {
	video := testVideo(t)
	b, err := os.ReadFile(video)
	if err != nil {
		t.Fatal(err)
	}
	st, w, fp, objs := setupWith(t, func(c *config.Analysis) {
		pv := c.Providers["fake"]
		pv.TranscribeModel = "stt-test"
		c.Providers["fake"] = pv
		c.Pricing["stt-test"] = config.ModelPrice{PerMinute: 60}
	})
	ctx := context.Background()
	post := &core.Item{ContentID: "tt:post:1", Platform: "tiktok", Kind: core.KindPost, SourceID: "tt:account:1", Version: 1,
		CreatedAt: time.Now().UTC().Add(-time.Hour), ObservedAt: time.Now().UTC(), Text: "فيديو جديد", Status: core.StatusVisible,
		Captions: "اتصلوا على 07701234567 للعرض", CaptionsLang: "ar", CaptionsSource: "platform_asr"}
	if _, err := st.Ingest(ctx, []*core.Item{post}, w.P.Plan); err != nil {
		t.Fatal(err)
	}
	objs["b/tt:media:v"] = b
	v := contractJob("tt:media:v", "content", "", time.Hour)
	v.Kind = "video"
	if _, err := st.InsertMediaJobs(ctx, []*core.MediaJob{v}, "analysis"); err != nil {
		t.Fatal(err)
	}
	var users []string
	fp.Fail = func(req provider.Request) error { users = append(users, req.User); return nil }
	if _, err := w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := st.DB.QueryRow(ctx, `SELECT count(*) FROM analysis.provider_requests WHERE model = 'stt-test'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("speech-to-text bought despite captions: %d %v", n, err)
	}
	var transcript, source, lang string
	if err := st.DB.QueryRow(ctx, `SELECT transcript, result->>'transcript_source', result->>'transcript_lang'
		FROM analysis.media_analyses WHERE media_id = 'tt:media:v'`).Scan(&transcript, &source, &lang); err != nil {
		t.Fatal(err)
	}
	if transcript != post.Captions || source != "platform_captions" || lang != "ar" {
		t.Fatalf("transcript %q from %q in %q", transcript, source, lang)
	}
	if len(users) != 1 || !strings.Contains(users[0], "للعرض") || strings.Contains(users[0], "07701234567") {
		t.Fatalf("vision request %q", users)
	}
	var payload []byte
	if err := st.DB.QueryRow(ctx, `SELECT payload FROM analysis.outbox WHERE topic = 'analysis.media.v1' AND key = 'tt:media:v'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var ev analysisv1.MediaAnalysis
	if err := proto.Unmarshal(payload, &ev); err != nil || ev.TranscriptSource != "platform_captions" || ev.TranscriptLang != "ar" ||
		ev.Transcript != post.Captions {
		t.Fatalf("published %v %v", &ev, err)
	}
}

// One transcription at a time: a video that needs speech-to-text while the
// slot is taken goes back to the queue for stt_retry_after, without
// counting a try, and comes back to be analysed with its transcript.
func TestWorkerSpeechToTextOneAtATime(t *testing.T) {
	video := testVideo(t)
	b, err := os.ReadFile(video)
	if err != nil {
		t.Fatal(err)
	}
	st, w, fp, objs := setupWith(t, func(c *config.Analysis) {
		c.Media.Video.STTConcurrency, c.Media.Video.STTWait, c.Media.Video.STTRetryAfter = 1, 20*time.Millisecond, time.Hour
	})
	ctx := context.Background()
	objs["b/v1"], objs["b/v2"], objs["b/i1"] = b, b, pngBytes(t, 100, 100)
	v1, v2, i1 := job("v1", "video"), job("v2", "video"), job("i1", "photo")
	// v1 is claimed first, then v2, then the image.
	v1.DeleteAfter, v2.DeleteAfter, i1.DeleteAfter = time.Now().Add(time.Hour), time.Now().Add(2*time.Hour), time.Now().Add(3*time.Hour)
	if _, err := st.InsertMediaJobs(ctx, []*core.MediaJob{v1, v2, i1}, "analysis"); err != nil {
		t.Fatal(err)
	}
	inside, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	fp.Transcribing = func(ctx context.Context) error {
		once.Do(func() { close(inside) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	done := make(chan error, 1)
	go func() { _, err := w.Once(ctx); done <- err }() // v1, held in speech-to-text
	select {
	case <-inside:
	case <-time.After(30 * time.Second):
		t.Fatal("v1 never reached speech-to-text")
	}
	// The slot is taken: the next claim takes the image, not v2 (on 12-13
	// Nov the workers kept claiming videos only to put them back).
	if _, err := w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	var tries int
	var claimed bool
	if err := st.DB.QueryRow(ctx, `SELECT state, tries, lease_until IS NOT NULL OR not_before > now() FROM analysis.media_jobs
		WHERE media_id = 'v2'`).Scan(&state, &tries, &claimed); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || tries != 0 || claimed {
		t.Fatalf("v2 while speech-to-text was busy: state %s, tries %d, claimed %v; want left alone", state, tries, claimed)
	}
	if err := st.DB.QueryRow(ctx, `SELECT state FROM analysis.media_jobs WHERE media_id = 'i1'`).Scan(&state); err != nil || state != "done" {
		t.Fatalf("the image while speech-to-text was busy: %s %v, want analysed", state, err)
	}
	if _, ok := acks(t, st)["v2"]; ok {
		t.Fatal("a deferred video was acknowledged")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var transcript string
	if err := st.DB.QueryRow(ctx, `SELECT coalesce(transcript, '') FROM analysis.media_analyses WHERE media_id = 'v1'`).Scan(&transcript); err != nil ||
		transcript == "" {
		t.Fatalf("v1 transcript %q %v", transcript, err)
	}
	// Due again, v2 now gets the free slot.
	if _, err := st.DB.Exec(ctx, `UPDATE analysis.media_jobs SET not_before = now() WHERE media_id = 'v2'`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.DB.QueryRow(ctx, `SELECT coalesce(transcript, '') FROM analysis.media_analyses WHERE media_id = 'v2'`).Scan(&transcript); err != nil ||
		transcript == "" {
		t.Fatalf("v2 transcript %q %v", transcript, err)
	}
}

// counting is an object store that counts fetches.
type counting struct {
	objects
	fetches int
}

func (c *counting) Fetch(ctx context.Context, bucket, key string, max int64, dst io.Writer) (int64, error) {
	c.fetches++
	return c.objects.Fetch(ctx, bucket, key, max, dst)
}

// With the day's budget already spent, a media job goes back to the queue
// before its file is fetched: no download, no decoding, no try counted,
// and its deadline keeps its place.
func TestWorkerBudgetSpentFetchesNothing(t *testing.T) {
	st, w, fp, objs := setupWith(t, func(c *config.Analysis) { c.Budget.DailyUSD = 1 })
	ctx := context.Background()
	objs["b/img"] = pngBytes(t, 100, 100)
	cnt := &counting{objects: objs}
	w.Objects = cnt
	if _, err := st.InsertMediaJobs(ctx, []*core.MediaJob{job("img", "photo")}, "analysis"); err != nil {
		t.Fatal(err)
	}
	// Today's spend is already over the $1 cap.
	if _, err := st.DB.Exec(ctx, `INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status, cost_usd)
		VALUES (gen_random_uuid(), 'comment', 'batch', 'fake', 'm', 1, 'done', 2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if cnt.fetches != 0 || fp.Calls() != 0 {
		t.Fatalf("budget spent: %d fetches, %d calls, want none", cnt.fetches, fp.Calls())
	}
	var state string
	var tries int
	var later bool
	if err := st.DB.QueryRow(ctx, `SELECT state, tries, not_before > now() FROM analysis.media_jobs WHERE media_id = 'img'`).Scan(&state, &tries, &later); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || tries != 0 || !later {
		t.Fatalf("job %s, %d tries, later %v: want pending, no try, back later", state, tries, later)
	}
}
