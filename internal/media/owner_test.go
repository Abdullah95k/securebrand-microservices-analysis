package media_test

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	contentv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/content/v1"
	fbv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/fb/v1"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/adapter"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/ingest"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/kafka"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/topics"
)

// waitForPosts turns the wait for a file's post back on (setup turns it off).
func waitForPosts(c *config.Analysis) {
	c.Media.OwnerWait, c.Media.OwnerMargin = 168*time.Hour, 12*time.Hour
}

// ingestPost stores a TikTok post through the ingest consumer, as Kafka
// would deliver it (no broker is reached: Flush is called directly).
func ingestPost(t *testing.T, st *store.Store, plan store.Planner, id, captions string) {
	t.Helper()
	c, err := ingest.New(ingest.Config{Kafka: kafka.ClientConfig{Brokers: []string{"127.0.0.1:1"}},
		Group: "test", Decoder: adapter.NewDecoder([]string{"content"}), Store: st, Plan: plan, Media: true, Consumer: "analysis"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	post := &contentv1.Post{
		Envelope:  &contentv1.Envelope{Id: id, Platform: "tt", Version: 1, ObservedAt: timestamppb.New(now)},
		AccountId: "tt:account:1", Text: &contentv1.Text{Text: "فيديو جديد"},
		PublishedAt: &contentv1.Time{At: timestamppb.New(now.Add(-time.Hour)), Precision: contentv1.TimePrecision_TIME_PRECISION_EXACT},
		Status:      &contentv1.Status{Availability: contentv1.Availability_AVAILABILITY_VISIBLE},
	}
	if captions != "" {
		post.Captions = []*contentv1.CaptionTrack{{Language: "ar", Source: contentv1.CaptionSource_CAPTION_SOURCE_PLATFORM_ASR, Text: captions}}
	}
	b, err := proto.Marshal(post)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Flush(context.Background(), []*kgo.Record{{Topic: topics.ContentPost, Value: b}}); err != nil {
		t.Fatal(err)
	}
}

// due reads a job's state, tries and how long until it is due (negative:
// due now).
func due(t *testing.T, st *store.Store, id string) (string, int, time.Duration) {
	t.Helper()
	var state string
	var tries int
	var secs float64
	if err := st.DB.QueryRow(context.Background(), `SELECT state, tries, extract(epoch FROM not_before - now())
		FROM analysis.media_jobs WHERE media_id = $1`, id).Scan(&state, &tries, &secs); err != nil {
		t.Fatal(err)
	}
	return state, tries, time.Duration(secs * float64(time.Second))
}

// TestWorkerWaitsForItsPost: a video announced before its post waits in
// the queue, without counting a try and before anything is fetched or
// bought; storing the post wakes it, and it goes ahead with the post's
// captions as its transcript (no speech-to-text).
func TestWorkerWaitsForItsPost(t *testing.T) {
	video := testVideo(t)
	b, err := os.ReadFile(video)
	if err != nil {
		t.Fatal(err)
	}
	st, w, fp, objs := setupWith(t, func(c *config.Analysis) {
		waitForPosts(c)
		pv := c.Providers["fake"]
		pv.TranscribeModel = "stt-test"
		c.Providers["fake"] = pv
		c.Pricing["stt-test"] = config.ModelPrice{PerMinute: 60}
	})
	ctx := context.Background()
	objs["b/tt:media:v"] = b
	cnt := &counting{objects: objs}
	w.Objects = cnt
	v := contractJob("tt:media:v", "content", "", 72*time.Hour)
	v.Kind = "video"
	if _, err := st.InsertMediaJobs(ctx, []*core.MediaJob{v}, "analysis"); err != nil {
		t.Fatal(err)
	}

	if _, err := w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if cnt.fetches != 0 || fp.Calls() != 0 {
		t.Fatalf("waiting for its post: %d fetches, %d calls, want none", cnt.fetches, fp.Calls())
	}
	state, tries, in := due(t, st, "tt:media:v")
	// Its wait ends 60 h after it arrived (12 h before its 72 h deadline);
	// it looks again at most 6 h later in case its wake was missed.
	if state != "pending" || tries != 0 || in < 5*time.Hour || in > 6*time.Hour+time.Minute {
		t.Fatalf("job %s, %d tries, due in %v: want pending, no try, due in about 6 h", state, tries, in)
	}
	if n, _ := w.Once(ctx); n != 0 {
		t.Fatal("a job waiting for its post was claimed again")
	}

	// The post arrives: ingest stores it and wakes the video.
	ingestPost(t, st, w.P.Plan, "tt:post:1", "النص المنطوق في الفيديو")
	if _, _, in := due(t, st, "tt:media:v"); in > 0 {
		t.Fatalf("stored post didn't wake its video: due in %v", in)
	}
	if _, err := w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	var transcript, source string
	if err := st.DB.QueryRow(ctx, `SELECT transcript, result->>'transcript_source' FROM analysis.media_analyses
		WHERE media_id = 'tt:media:v'`).Scan(&transcript, &source); err != nil {
		t.Fatal(err)
	}
	if transcript != "النص المنطوق في الفيديو" || source != "platform_captions" {
		t.Fatalf("transcript %q from %q, want the post's captions", transcript, source)
	}
	var stt int
	if err := st.DB.QueryRow(ctx, `SELECT count(*) FROM analysis.provider_requests WHERE model = 'stt-test'`).Scan(&stt); err != nil || stt != 0 {
		t.Fatalf("speech-to-text bought for a video with captions: %d %v", stt, err)
	}
}

// TestWorkerStopsWaitingNearTheDeadline: a file whose deadline is within
// owner_margin, or whose post has been waited for owner_wait, goes ahead
// without its post; a file without an owner never waits.
func TestWorkerStopsWaitingNearTheDeadline(t *testing.T) {
	st, w, fp, objs := setupWith(t, waitForPosts)
	ctx := context.Background()
	objs["b/near"] = pngBytes(t, 100, 100)
	objs["b/old"] = pngBytes(t, 100, 100)
	objs["b/none"] = pngBytes(t, 100, 100)
	near := contractJob("near", "content", "", 6*time.Hour) // inside the 12 h margin
	near.OwnerContentID = "tt:post:2"
	old := contractJob("old", "content", "", 0) // no deadline, arrived 8 days ago
	old.OwnerContentID, old.DeleteAfter = "tt:post:3", time.Time{}
	none := contractJob("none", "content", "", 72*time.Hour)
	none.OwnerContentID = ""
	if _, err := st.InsertMediaJobs(ctx, []*core.MediaJob{near, old, none}, "analysis"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.Exec(ctx, `UPDATE analysis.media_jobs SET received_at = now() - interval '8 days' WHERE media_id = 'old'`); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := w.Once(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"near", "old", "none"} {
		if state, _, _ := due(t, st, id); state != "done" {
			t.Errorf("%s is %s, want analysed without waiting", id, state)
		}
	}
	if fp.Calls() != 3 {
		t.Fatalf("%d calls, want 3", fp.Calls())
	}
}

// TestHoldSeesAPostStoredMeanwhile: if the post is stored between the
// worker's look and its hold, the hold makes the file due at once instead
// of leaving it to wait for a wake that has already passed.
func TestHoldSeesAPostStoredMeanwhile(t *testing.T) {
	st, w, _, _ := setupWith(t, waitForPosts)
	ctx := context.Background()
	if _, err := st.InsertMediaJobs(ctx, []*core.MediaJob{contractJob("m", "content", "", 72*time.Hour)}, "analysis"); err != nil {
		t.Fatal(err)
	}
	ingestPost(t, st, w.P.Plan, "tt:post:1", "")
	if err := st.HoldMediaForOwner(ctx, "m", "tt:post:1", time.Now().Add(6*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, in := due(t, st, "m"); in > 0 {
		t.Fatalf("held for a post already stored: due in %v", in)
	}
	// A wake touches only jobs that wait: not one leased or finished.
	if _, err := st.DB.Exec(ctx, `UPDATE analysis.media_jobs SET not_before = now() + interval '1 hour', lease_until = now() + interval '10 minutes'
		WHERE media_id = 'm'`); err != nil {
		t.Fatal(err)
	}
	if n, err := st.WakeMediaOf(ctx, []string{"tt:post:1"}); err != nil || n != 0 {
		t.Fatalf("woke %d leased jobs (%v), want 0", n, err)
	}
}

// TestReuseWaitsForItsPost: an identical file analysed before is reused
// only once the new file's post is stored, so the copy is dated by its own
// post.
func TestReuseWaitsForItsPost(t *testing.T) {
	st, w, fp, objs := setupWith(t, waitForPosts)
	ctx := context.Background()
	objs["b/first"] = pngBytes(t, 100, 100)
	ingestPost(t, st, w.P.Plan, "tt:post:1", "")
	first := contractJob("first", "content", "sha-1", 72*time.Hour)
	if _, err := st.InsertMediaJobs(ctx, []*core.MediaJob{first}, "analysis"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	second := contractJob("second", "content", "sha-1", 72*time.Hour)
	second.OwnerContentID = "tt:post:4"
	if _, err := st.InsertMediaJobs(ctx, []*core.MediaJob{second}, "analysis"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if state, tries, in := due(t, st, "second"); state != "pending" || tries != 0 || in <= 0 {
		t.Fatalf("copy before its post: %s, %d tries, due in %v; want waiting", state, tries, in)
	}
	ingestPost(t, st, w.P.Plan, "tt:post:4", "")
	if _, err := w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	var from string
	if err := st.DB.QueryRow(ctx, `SELECT coalesce(reused_from, '') FROM analysis.media_analyses WHERE media_id = 'second'`).Scan(&from); err != nil {
		t.Fatal(err)
	}
	if from != "first" || fp.Calls() != 1 {
		t.Fatalf("reused from %q with %d calls, want a copy of first and one call", from, fp.Calls())
	}
}

// TestCommentOutsideSampleDoesNotWait: a file whose comment is outside the
// comment sample (counted, never stored) is analysed at once, without the
// comment's text; one whose comment is in the sample waits for it. Waking
// the comments' files makes a held one due again (on 13 Nov such files
// waited up to 7 days for comments that would never come).
func TestCommentOutsideSampleDoesNotWait(t *testing.T) {
	st, w, fp, objs := setupWith(t, waitForPosts)
	st.CommentShare = 0.10
	ctx := context.Background()
	var in, out string
	for i := 0; in == "" || out == ""; i++ {
		id := fmt.Sprintf("tt:comment:%d", i)
		if core.InSample(id, 0.10) {
			in = id
		} else {
			out = id
		}
	}
	objs["b/in"], objs["b/out"] = pngBytes(t, 100, 100), pngBytes(t, 100, 100)
	a, b := contractJob("in", "attachment", "", 72*time.Hour), contractJob("out", "attachment", "", 72*time.Hour)
	a.OwnerContentID, b.OwnerContentID = in, out
	if _, err := st.InsertMediaJobs(ctx, []*core.MediaJob{a, b}, "analysis"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := w.Once(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if state, _, _ := due(t, st, "out"); state != "done" {
		t.Fatalf("the file of a comment outside the sample is %s, want analysed at once", state)
	}
	if state, tries, in := due(t, st, "in"); state != "pending" || tries != 0 || in <= 0 {
		t.Fatalf("the file of a sampled comment: %s, %d tries, due in %v; want waiting for it", state, tries, in)
	}
	if fp.Calls() != 1 {
		t.Fatalf("%d calls, want 1", fp.Calls())
	}
	if n, err := st.WakeCommentMedia(ctx); err != nil || n != 1 {
		t.Fatalf("woke %d (%v), want the held one", n, err)
	}
	if _, _, in := due(t, st, "in"); in > 0 {
		t.Fatalf("still due in %v after the wake", in)
	}
}

// TestProfilePicturesOfMonitoredAccountsOnly (D-061): a profile picture or
// banner is analysed when its account is the source of a stored item (a
// monitored publisher), with the role's note in the request and without
// waiting for an owner that is never stored; a commenter's is acknowledged
// SKIPPED, policy:not_monitored.
func TestProfilePicturesOfMonitoredAccountsOnly(t *testing.T) {
	st, w, fp, objs := setupWith(t, waitForPosts)
	ctx := context.Background()
	ingestPost(t, st, w.P.Plan, "tt:post:1", "") // tt:account:1's post
	var mu sync.Mutex
	users := map[string]string{}
	fp.Fail = func(req provider.Request) error {
		mu.Lock()
		defer mu.Unlock()
		for _, id := range []string{"mon", "ban"} {
			if strings.Contains(req.User, "profile picture") && id == "mon" || strings.Contains(req.User, "banner") && id == "ban" {
				users[id] = req.User
			}
		}
		return nil
	}
	mon, ban, other := contractJob("mon", "profile_picture", "", 72*time.Hour), contractJob("ban", "banner", "", 72*time.Hour),
		contractJob("other", "profile_picture", "", 72*time.Hour)
	mon.OwnerContentID, ban.OwnerContentID = "tt:account:1", "tt:account:1"
	other.OwnerContentID, other.SourceID = "tt:account:2", "tt:account:2"
	for _, id := range []string{"mon", "ban", "other"} {
		objs["b/"+id] = pngBytes(t, 200, 200)
	}
	if _, err := st.InsertMediaJobs(ctx, []*core.MediaJob{mon, ban, other}, "analysis"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := w.Once(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for id, want := range map[string]string{"mon": "done", "ban": "done", "other": "skipped"} {
		var state, reason string
		if err := st.DB.QueryRow(ctx, `SELECT state, coalesce(last_error, '') FROM analysis.media_jobs WHERE media_id = $1`, id).Scan(&state, &reason); err != nil {
			t.Fatal(err)
		}
		if state != want || (want == "skipped" && reason != "policy:not_monitored") {
			t.Errorf("%s: %s (%q), want %s", id, state, reason, want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for id, want := range map[string]string{"mon": "profile picture of a monitored", "ban": "banner (cover image) of a monitored"} {
		if u := users[id]; !strings.Contains(u, want) || !strings.Contains(u, "Never say who a person in it is") {
			t.Errorf("%s's request lacks its note: %q", id, u)
		}
	}
}

// TestThumbnailsOnlyForYouTube (D-061): a YouTube video's thumbnail is
// analysed, at media.thumbnail_max_side; another platform's is skipped (its
// video is analysed itself).
func TestThumbnailsOnlyForYouTube(t *testing.T) {
	st, w, fp, objs := setupWith(t, nil)
	ctx := context.Background()
	var mu sync.Mutex
	var side int
	fp.Fail = func(req provider.Request) error {
		if len(req.Images) == 1 {
			if c, _, err := image.DecodeConfig(bytes.NewReader(req.Images[0].Data)); err == nil {
				mu.Lock()
				side = max(c.Width, c.Height)
				mu.Unlock()
			}
		}
		return nil
	}
	yt, tt := contractJob("yt", "thumbnail", "", 72*time.Hour), contractJob("ttt", "thumbnail", "", 72*time.Hour)
	yt.Platform, yt.OwnerContentID, yt.SourceID = "youtube", "yt:post:1", "yt:account:1"
	objs["b/yt"], objs["b/ttt"] = pngBytes(t, 1280, 720), pngBytes(t, 1280, 720)
	if _, err := st.InsertMediaJobs(ctx, []*core.MediaJob{yt, tt}, "analysis"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := w.Once(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var state, reason string
	if err := st.DB.QueryRow(ctx, `SELECT state FROM analysis.media_jobs WHERE media_id = 'yt'`).Scan(&state); err != nil || state != "done" {
		t.Fatalf("YouTube's thumbnail: %s %v, want analysed", state, err)
	}
	if err := st.DB.QueryRow(ctx, `SELECT state, coalesce(last_error, '') FROM analysis.media_jobs WHERE media_id = 'ttt'`).Scan(&state, &reason); err != nil ||
		state != "skipped" || reason != "policy:image/thumbnail" {
		t.Fatalf("TikTok's thumbnail: %s %q %v, want skipped by policy", state, reason, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if side != 512 {
		t.Fatalf("thumbnail sent at %d px, want 512", side)
	}
}

// TestFacebookPagePictureDoesNotWait (D-061): fbs's page profile picture
// (owned by the page, no post) is analysed at once, with the profile
// picture's note; it doesn't wait for a post that doesn't exist.
func TestFacebookPagePictureDoesNotWait(t *testing.T) {
	st, w, fp, objs := setupWith(t, waitForPosts)
	ctx := context.Background()
	var mu sync.Mutex
	var user string
	fp.Fail = func(req provider.Request) error {
		mu.Lock()
		user = req.User
		mu.Unlock()
		return nil
	}
	j := adapter.FromFBMediaReady(&fbv1.MediaReady{MediaId: "pagepic", Kind: fbv1.MediaKind_MEDIA_KIND_PROFILE_PIC, PageId: 9,
		Object: &fbv1.S3Object{Bucket: "b", Key: "pagepic"}, DeleteAfter: timestamppb.New(time.Now().Add(72 * time.Hour))})
	objs["b/pagepic"] = pngBytes(t, 200, 200)
	if _, err := st.InsertMediaJobs(ctx, []*core.MediaJob{j}, "analysis"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Once(ctx); err != nil {
		t.Fatal(err)
	}
	if state, _, _ := due(t, st, "pagepic"); state != "done" {
		t.Fatalf("the page's profile picture is %s, want analysed at once", state)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(user, "profile picture of a monitored") {
		t.Fatalf("its request lacks the note: %q", user)
	}
}
