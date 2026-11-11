package app_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/types/known/timestamppb"

	fbv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/fb/v1"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/adapter"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/ametrics"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/app"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/ingest"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/kafka"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/testenv"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/topics"
)

// At the queue's cap ingest stops taking comments: they wait in Kafka,
// while posts (few, and their media waits for them) and erasures still get
// through; once the queue drains, the rest arrives.
func TestBackpressurePausesIngest(t *testing.T) {
	db := testenv.Postgres(t)
	brokers := testenv.Kafka(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Kafka.TopicPrefix = testenv.TopicPrefix()
	cfg.Analysis.ConsumerGroup = fmt.Sprintf("sb-analysis-bp-%d", rand.Int64N(1<<30))
	kc := kafka.ClientConfig{Brokers: brokers, ClientID: "bp", TopicPrefix: cfg.Kafka.TopicPrefix}
	a, err := app.New(ctx, cfg, db, kc)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Prepare(ctx, true); err != nil {
		t.Fatal(err)
	}
	prod, err := kafka.NewProducer(kc)
	if err != nil {
		t.Fatal(err)
	}
	defer prod.Close()
	now := time.Now().UTC().Add(-time.Hour)
	obs := &fbv1.Observation{ObservedAt: timestamppb.New(now)}
	const comments = 12
	for i := range comments {
		rec, err := prod.Record(topics.Comment, "1", &fbv1.Comment{Obs: obs, CommentId: int64(100 + i), PostId: 1, PageId: 9,
			PostCreatedAt: timestamppb.New(now), CreatedAt: timestamppb.New(now), Text: fmt.Sprintf("تعليق رقم %d عن الخدمة", i),
			Author: &fbv1.Author{AuthorKey: []byte(fmt.Sprintf("a%d", i))}})
		if err != nil {
			t.Fatal(err)
		}
		if err := prod.ProduceSync(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}

	gate := &ingest.Gate{Max: 4, Resume: 1, Every: 0, Depth: a.Store.QueueDepth}
	c, err := ingest.New(ingest.Config{Kafka: kc, Group: cfg.Analysis.ConsumerGroup, Decoder: adapter.NewDecoder([]string{"fb"}),
		Store: a.Store, Plan: a.Pipeline.Plan, Consumer: cfg.Analysis.Media.Consumer, MaxBatch: 2, Linger: 200 * time.Millisecond, Gate: gate})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = c.Run(runCtx) }()
	defer func() { stop(); wg.Wait() }()

	ingested := func() int { return count(t, a, `SELECT count(*) FROM analysis.content_items`) }
	waitFor(t, ctx, func() bool { return testutil.ToFloat64(ametrics.IngestPaused) == 1 })
	held := ingested()
	if depth, _ := a.Store.QueueDepth(ctx); depth < 4 || depth > 4+2 {
		t.Fatalf("paused at queue depth %d, want the cap (4) plus at most one batch (2)", depth)
	}
	time.Sleep(2 * time.Second)
	if got := ingested(); got != held || held >= comments {
		t.Fatalf("ingest went on while full: %d items, then %d (of %d)", held, got, comments)
	}
	// Posts keep flowing while comments wait.
	post, err := prod.Record(topics.Post, "2", &fbv1.Post{Obs: obs, PostId: 2, PageId: 9, CreatedAt: timestamppb.New(now),
		Text: "منشور جديد عن العروض"})
	if err != nil {
		t.Fatal(err)
	}
	if err := prod.ProduceSync(ctx, post); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		return count(t, a, `SELECT count(*) FROM analysis.content_items WHERE content_id = 'fb:post:2'`) == 1
	})
	if got := count(t, a, `SELECT count(*) FROM analysis.content_items WHERE kind <> 'post'`); got != held {
		t.Fatalf("comments ingested while full: %d, were %d", got, held)
	}
	// Erasures keep flowing while comments wait.
	rec, err := prod.Record(topics.Erasure, "r1", &fbv1.ErasureEvent{EventId: "r1", RequestId: "r1",
		SubjectType: fbv1.SubjectType_SUBJECT_TYPE_AUTHOR, AuthorKey: []byte("a0")})
	if err != nil {
		t.Fatal(err)
	}
	if err := prod.ProduceSync(ctx, rec); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool { return count(t, a, `SELECT count(*) FROM analysis.erasures WHERE request_id = 'r1'`) == 1 })
	// The analysis drains the queue (here: by hand); the rest comes in.
	waitFor(t, ctx, func() bool {
		_, _ = db.Exec(ctx, `DELETE FROM analysis.analysis_queue`)
		return ingested() == comments+1
	})
	t.Logf("paused with %d of %d comments ingested; all %d after the queue drained", held, comments, comments)
}
