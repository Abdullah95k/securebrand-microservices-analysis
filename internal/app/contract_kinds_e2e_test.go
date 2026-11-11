package app_test

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	contracts "github.com/abdullah95k/securebrand-contracts"
	"github.com/abdullah95k/securebrand-contracts/conformance"
	analysisv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/analysis/v1"
	contentv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/content/v1"
	mediav1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/media/v1"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/media"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/kafka"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/relay"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/topics"
)

// files is a fake object store for the media role.
type files map[string][]byte

func (f files) Fetch(_ context.Context, bucket, key string, max int64, dst io.Writer) (int64, error) {
	b, ok := f[bucket+"/"+key]
	if !ok {
		return 0, media.ErrGone
	}
	return media.CopyLimited(dst, bytes.NewReader(b), max)
}

func pngFile(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for x := 0; x < 64; x++ {
		img.Set(x, x%48, color.RGBA{200, 0, 0, 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// TestContractEveryKindEndToEnd: a fake platform publishes the contract's
// content and media events, including a post whose time is an estimate
// (D-013: a day's estimate sits at 00:00 Iraq time) and a partial
// deletion; sb-analysis runs ingest, pipeline, media and relay against
// real Postgres and Redpanda; the owner's bot scoring and a topic approval
// run as their commands do. Every kind sb-analysis produces then comes off
// Kafka with the contract's headers and passes the contract's own
// conformance checks in production mode: ItemAnalysis, MediaAnalysis,
// AuthorFlag, TopicUpdate and MediaProcessed.
func TestContractEveryKindEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	e := newE2E(t, ctx, []string{"content"})
	a := e.app
	pf := fakePlatform{t: t, prod: e.prod}
	img := pngFile(t)
	a.Objects = files{"media/fb/1.png": img, "media/fb/2.webp": img, "media/fb/3.png": img}

	iraq := time.FixedZone("Asia/Baghdad", 3*3600)
	// An hour ago, but the spam below spans the 32 minutes before it, and
	// bot scoring counts by Iraq day: on two days (the first 33 minutes
	// after 01:00 Iraq time) it wasn't flagged. oneIraqDay keeps it on one.
	now := oneIraqDay(time.Now().UTC().Add(-time.Hour).Truncate(time.Second), 40*time.Minute)
	y, m, d := now.In(iraq).AddDate(0, 0, -1).Date()
	estimate := time.Date(y, m, d, 0, 0, 0, 0, iraq) // "yesterday": 00:00 Iraq time
	v := now.UnixMicro()
	text := func(s string) *contentv1.Text { return &contentv1.Text{Text: s, PlatformLanguage: "ar"} }
	visible := &contentv1.Status{Availability: contentv1.Availability_AVAILABILITY_VISIBLE}

	pf.send(ctx, topics.ContentPost, "fb:post:1", &contentv1.Post{Envelope: envelope("post", "fb:post:1", "1", now, v),
		AccountId: "fb:account:9", Account: &contentv1.AccountRef{Id: "fb:account:9", DisplayName: "Asiacell"},
		Text: text("عرض جديد"), Status: visible,
		PublishedAt: &contentv1.Time{At: timestamppb.New(estimate), Precision: contentv1.TimePrecision_TIME_PRECISION_DAY, Displayed: "1d"}})
	// One account posting the same text 32 times in half an hour, across
	// four posts: a bot for the owner's scoring.
	spam := "اربح جوال ايفون مجانا ادخل على الرابط الان وسجل رقمك"
	for i := range 32 {
		id := fmt.Sprintf("fb:comment:%d", 100+i)
		post := fmt.Sprintf("fb:post:%d", 1+i%4)
		at := now.Add(-time.Duration(i) * time.Minute)
		c := &contentv1.Comment{Envelope: envelope("comment", id, fmt.Sprint(100+i), at, v), PostId: post, AccountId: "fb:account:9",
			Author: &contentv1.Author{Account: &contentv1.AccountRef{Id: "fb:account:666"}}, Text: text(spam), Status: visible,
			PublishedAt: published(at)}
		pf.send(ctx, topics.ContentComment, post, c)
	}
	pf.send(ctx, topics.ContentComment, "fb:post:1", &contentv1.Comment{Envelope: envelope("comment", "fb:comment:50", "50", now, v),
		PostId: "fb:post:1", AccountId: "fb:account:9", Author: &contentv1.Author{Account: &contentv1.AccountRef{Id: "fb:account:501"}},
		Text: text("اسياسيل خايسة"), Status: visible, PublishedAt: published(now)})
	// Three files: an image to analyse, a sticker (skipped by policy), and
	// an image the platform deletes before it is analysed.
	ready := func(n int, typ contentv1.MediaType, role contentv1.MediaRole, key string) *mediav1.MediaReady {
		return &mediav1.MediaReady{EventId: fmt.Sprintf("evt-media-%d", n), Platform: "fb", MediaId: fmt.Sprintf("fb:media:%d", n),
			OwnerId: "fb:post:1", OwnerKind: "post", AccountId: "fb:account:9", Type: typ, Role: role,
			Object:   &mediav1.StorageObject{Bucket: "media", Key: key, SizeBytes: int64(len(img)), Sha256: fmt.Sprintf("%064d", n)},
			StoredAt: timestamppb.New(now), DeleteAfter: timestamppb.New(now.Add(time.Duration(70+n) * time.Hour)),
			RequiredConsumers: []string{store.ContractConsumer}, Attempt: 1}
	}
	pf.send(ctx, topics.ContentMediaReady, "fb:media:1", ready(1, contentv1.MediaType_MEDIA_TYPE_IMAGE, contentv1.MediaRole_MEDIA_ROLE_CONTENT, "fb/1.png"))
	pf.send(ctx, topics.ContentMediaReady, "fb:media:2", ready(2, contentv1.MediaType_MEDIA_TYPE_STICKER, contentv1.MediaRole_MEDIA_ROLE_ATTACHMENT, "fb/2.webp"))
	pf.send(ctx, topics.ContentMediaReady, "fb:media:3", ready(3, contentv1.MediaType_MEDIA_TYPE_IMAGE, contentv1.MediaRole_MEDIA_ROLE_CONTENT, "fb/3.png"))
	pf.send(ctx, topics.ContentMediaDeleted, "fb:media:3", &mediav1.MediaDeleted{EventId: "evt-del-3", MediaId: "fb:media:3",
		Reason: mediav1.DeleteReason_DELETE_REASON_EXPIRED, DeletedAt: timestamppb.New(now), Platform: "fb"})

	stop := e.run(t, ctx, "ingest", "pipeline", "relay")
	defer stop()
	waitFor(t, ctx, func() bool {
		return count(t, a, `SELECT count(*) FROM analysis.analyses WHERE is_current`) == 34 &&
			count(t, a, `SELECT count(*) FROM analysis.media_jobs`) == 3 &&
			count(t, a, `SELECT count(*) FROM analysis.media_jobs WHERE media_id = 'fb:media:3' AND state = 'gone'`) == 1
	})
	// The media role starts after the deletion is in, so the deleted file
	// is never fetched.
	stopMedia := e.run(t, ctx, "media")
	defer stopMedia()
	waitFor(t, ctx, func() bool {
		return count(t, a, `SELECT count(*) FROM analysis.media_jobs WHERE state IN ('done', 'skipped')`) == 2
	})
	// The owner's commands: bot scoring now, and a new topic approved.
	if run, err := a.ScoreBots(ctx); err != nil || run.Flagged == 0 {
		t.Fatalf("bot scoring: %+v %v", run, err)
	}
	ids, err := a.Store.ResolveTopics(ctx, [][]core.TopicProposal{{{EN: "Water shortages", Parent: "services"}}}, nil)
	if err != nil || len(ids) != 1 || len(ids[0]) != 1 {
		t.Fatalf("topic proposal %v %v", ids, err)
	}
	if _, err := a.Store.ApplyTopicDecisions(ctx, []store.TopicDecision{{ID: ids[0][0], Action: "approve"}}, "owner approve"); err != nil {
		t.Fatal(err)
	}
	// A partial deletion of the normal comment: marked deleted, kept.
	del := &contentv1.Comment{Envelope: envelope("comment", "fb:comment:50", "50", now.Add(time.Minute), v+1), PostId: "fb:post:1",
		AccountId: "fb:account:9", Status: &contentv1.Status{Availability: contentv1.Availability_AVAILABILITY_DELETED}}
	del.Envelope.Partial = true
	pf.send(ctx, topics.ContentComment, "fb:post:1", del)
	waitFor(t, ctx, func() bool {
		return count(t, a, `SELECT count(*) FROM analysis.content_items WHERE content_id = 'fb:comment:50' AND status = 'deleted'`) == 1 &&
			count(t, a, `SELECT count(*) FROM analysis.outbox`) == 0
	})
	// The estimated time is the post's time, as the platform gave it.
	if n := count(t, a, fmt.Sprintf(`SELECT count(*) FROM analysis.content_items WHERE content_id = 'fb:post:1'
		AND created_at = '%s'`, estimate.UTC().Format(time.RFC3339))); n != 1 {
		t.Fatal("the post's estimated time was not kept")
	}

	prefix := e.cfg.Kafka.TopicPrefix
	prod := conformance.Options{Production: true}
	// Every kind, off Kafka, with the contract's headers and checks.
	results := consume(t, ctx, e.brokers, prefix+topics.AnalysisResult, 34)
	for id, r := range results {
		var m analysisv1.ItemAnalysis
		decode(t, r, &m)
		if err := conformance.ItemAnalysis(&m, prod); err != nil {
			t.Errorf("%s: %v", id, err)
		}
	}
	var post analysisv1.ItemAnalysis
	decode(t, results["fb:post:1"], &post)
	if !post.GetCreatedAt().AsTime().Equal(estimate) {
		t.Errorf("post created_at %v, want the estimate %v", post.GetCreatedAt().AsTime(), estimate)
	}
	mediaRes := consume(t, ctx, e.brokers, prefix+topics.AnalysisMedia, 1)
	var ma analysisv1.MediaAnalysis
	decode(t, mediaRes["fb:media:1"], &ma)
	if err := conformance.MediaAnalysis(&ma, prod); err != nil {
		t.Error(err)
	}
	flags := consume(t, ctx, e.brokers, prefix+topics.AnalysisAuthor, 1)
	var af analysisv1.AuthorFlag
	decode(t, flags["fb:account:666"], &af)
	if err := conformance.AuthorFlag(&af, prod); err != nil || af.GetBot() == "" {
		t.Errorf("author flag %v: %v", &af, err)
	}
	tops := consume(t, ctx, e.brokers, prefix+topics.AnalysisTopic, 1)
	var tu analysisv1.TopicUpdate
	decode(t, tops[ids[0][0]], &tu)
	if err := conformance.TopicUpdate(&tu, prod); err != nil || tu.GetStatus() != "active" {
		t.Errorf("topic update %v: %v", &tu, err)
	}
	acks := consume(t, ctx, e.brokers, prefix+topics.ContentMediaProcessed, 2)
	for id, want := range map[string]mediav1.ProcessStatus{"fb:media:1": mediav1.ProcessStatus_PROCESS_STATUS_PROCESSED,
		"fb:media:2": mediav1.ProcessStatus_PROCESS_STATUS_SKIPPED} {
		var mp mediav1.MediaProcessed
		decode(t, acks[id], &mp)
		if err := conformance.MediaProcessed(&mp, prod); err != nil || mp.GetStatus() != want {
			t.Errorf("%s acknowledged %v: %v", id, &mp, err)
		}
	}
	if acks["fb:media:3"] != nil {
		t.Error("a deleted file was acknowledged")
	}
}

// decode checks a record's contract headers and decodes it.
func decode(t *testing.T, r *kgo.Record, m proto.Message) {
	t.Helper()
	if r == nil {
		t.Fatalf("no %s record", proto.MessageName(m))
	}
	if h := kafka.HeaderValue(r, relay.HeaderSBType); h != string(proto.MessageName(m)) {
		t.Errorf("%s: sb-type %q", r.Key, h)
	}
	if h := kafka.HeaderValue(r, relay.HeaderSBContract); h != contracts.Version {
		t.Errorf("%s: sb-contract %q", r.Key, h)
	}
	if err := proto.Unmarshal(r.Value, m); err != nil {
		t.Fatalf("%s: %v", r.Key, err)
	}
}

// oneIraqDay moves t, if span before it would cross an Iraq midnight
// (UTC+3), to span after that midnight: still in the past when t is an
// hour or more ago and span is under an hour.
func oneIraqDay(t time.Time, span time.Duration) time.Time {
	iraq := time.FixedZone("Iraq", 3*3600)
	l := t.In(iraq)
	midnight := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, iraq)
	if t.Sub(midnight) < span {
		return midnight.Add(span).UTC()
	}
	return t
}

func TestOneIraqDay(t *testing.T) {
	span := 40 * time.Minute
	for in, want := range map[string]string{
		"2026-11-11T21:20:00Z": "2026-11-11T21:40:00Z", // 00:20 Iraq: moved past 00:40
		"2026-11-11T21:50:00Z": "2026-11-11T21:50:00Z", // 00:50 Iraq: kept
		"2026-11-11T09:00:00Z": "2026-11-11T09:00:00Z", // noon Iraq: kept
	} {
		at, _ := time.Parse(time.RFC3339, in)
		if got := oneIraqDay(at, span).Format(time.RFC3339); got != want {
			t.Errorf("oneIraqDay(%s) = %s, want %s", in, got, want)
		}
	}
}
