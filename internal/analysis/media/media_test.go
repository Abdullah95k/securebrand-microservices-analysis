package media_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	fbv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/fb/v1"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/media"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/pipeline"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/provider"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/provider/fake"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/provider/registry"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/store"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/taxonomy"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/textnorm"
	"github.com/abdullah95k/securebrand-microservices/internal/config"
	"github.com/abdullah95k/securebrand-microservices/internal/testenv"
	"github.com/abdullah95k/securebrand-microservices/internal/topics"
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
	out, err := media.Resize(pngBytes(t, 3000, 1500), 1024)
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
	small, _ := media.Resize(pngBytes(t, 300, 600), 1024)
	img, _ = jpeg.Decode(bytes.NewReader(small))
	if b := img.Bounds(); b.Dx() != 300 || b.Dy() != 600 {
		t.Fatalf("small image resized to %v", b)
	}
	tall, _ := media.Resize(pngBytes(t, 500, 2000), 1000)
	img, _ = jpeg.Decode(bytes.NewReader(tall))
	if b := img.Bounds(); b.Dx() != 250 || b.Dy() != 1000 {
		t.Fatalf("tall image resized to %v", b)
	}
	if _, err := media.Resize([]byte("not an image"), 100); err == nil {
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

func (o objects) Get(_ context.Context, bucket, key string, max int64) ([]byte, error) {
	b, ok := o[bucket+"/"+key]
	if !ok {
		return nil, media.ErrGone
	}
	if int64(len(b)) > max {
		return nil, media.ErrTooLarge
	}
	return b, nil
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
	canon := job("canon", "image") // canonical content.media.ready.v1 kind
	canon.AckFormat = "content"
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
	_ = st.DB.QueryRow(ctx, `SELECT count(*) FROM analysis.media_analyses WHERE media_id IN ('img', 'canon') AND description <> ''`).Scan(&n)
	if n != 2 {
		t.Fatal("image analyses not stored (Facebook photo and canonical image)")
	}
	_ = st.DB.QueryRow(ctx, `SELECT count(*) FROM analysis.outbox WHERE topic = 'content.media.processed.v1' AND key = 'canon'`).Scan(&n)
	if n != 1 {
		t.Fatal("canonical image not acknowledged on content.media.processed.v1")
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
