package app_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/app"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/kafka"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/testenv"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/topics"
	fbv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/fb/v1"
)

// TestFacebookToAnalysisEndToEnd runs ingest, pipeline (fake provider) and
// relay against real Postgres and Redpanda: Facebook records in, analyses
// in Postgres (not on the contract's topics), then an author erasure.
func TestFacebookToAnalysisEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	e := newE2E(t, ctx, []string{"fb"})
	a, cfg, prod := e.app, e.cfg, e.prod
	now := time.Now().UTC().Add(-time.Hour)
	obs := &fbv1.Observation{ObservedAt: timestamppb.New(now)}
	send := func(topic, key string, m proto.Message) {
		rec, err := prod.Record(topic, key, m)
		if err != nil {
			t.Fatal(err)
		}
		if err := prod.ProduceSync(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}
	send(topics.Post, "1", &fbv1.Post{Obs: obs, PostId: 1, PageId: 9, AuthorName: "Asiacell", CreatedAt: timestamppb.New(now), Text: "عرض جديد"})
	send(topics.Comment, "1", &fbv1.Comment{Obs: obs, CommentId: 11, PostId: 1, PageId: 9, PostCreatedAt: timestamppb.New(now),
		CreatedAt: timestamppb.New(now), Text: "اسياسيل خايسة", Author: &fbv1.Author{AuthorKey: []byte("author-1")}})
	send(topics.Comment, "1", &fbv1.Comment{Obs: obs, CommentId: 12, PostId: 1, PageId: 9, PostCreatedAt: timestamppb.New(now),
		CreatedAt: timestamppb.New(now), Text: "شكرا", Author: &fbv1.Author{AuthorKey: []byte("author-2")}})

	stop := e.run(t, ctx, "ingest", "pipeline", "relay")
	defer stop()

	waitFor(t, ctx, func() bool {
		return count(t, a, `SELECT count(*) FROM analysis.analyses WHERE is_current`) == 3
	})
	if n := count(t, a, `SELECT count(*) FROM analysis.analyses WHERE content_id = 'fb:comment:11' AND is_current
		AND sentiment = 'negative'`); n != 1 {
		t.Fatal("comment 11 not analysed as negative")
	}
	if n := count(t, a, `SELECT count(*) FROM analysis.entity_mentions WHERE content_id = 'fb:comment:11' AND entity_id = 'asiacell'`); n != 1 {
		t.Fatal("comment 11 not linked to asiacell")
	}
	// Items from fbs's own topics have no contract IDs: their results stay
	// in Postgres and never reach the contract's analysis.result.v1.
	if n := count(t, a, `SELECT count(*) FROM analysis.outbox WHERE topic = 'analysis.result.v1'`); n != 0 {
		t.Fatalf("%d results of fbs's own items queued for the contract's topic", n)
	}
	// An erasure of author-1 is only recorded by default (the owner keeps
	// all data): nothing is deleted until the owner applies it.
	send(topics.Erasure, "r1", &fbv1.ErasureEvent{EventId: "r1", RequestId: "r1", SubjectType: fbv1.SubjectType_SUBJECT_TYPE_AUTHOR,
		AuthorKey: []byte("author-1")})
	waitFor(t, ctx, func() bool {
		return count(t, a, `SELECT count(*) FROM analysis.erasures WHERE request_id = 'r1' AND mode = 'record' AND done_at IS NULL`) == 1
	})
	if n := count(t, a, `SELECT count(*) FROM analysis.analyses WHERE content_id = 'fb:comment:11'`); n != 1 {
		t.Fatal("a recorded erasure deleted the analysis")
	}
	// Applied on the owner's order, it removes their comment's analysis.
	if _, err := a.Store.ApplyRecordedErasure(ctx, "r1", cfg.Analysis.Media.Consumer); err != nil {
		t.Fatal(err)
	}
	if n := count(t, a, `SELECT count(*) FROM analysis.analyses WHERE content_id = 'fb:comment:11'`); n != 0 {
		t.Fatal("erased author's analysis remains")
	}
	if n := count(t, a, `SELECT count(*) FROM analysis.analyses WHERE content_id = 'fb:comment:12'`); n != 1 {
		t.Fatal("other author's analysis removed")
	}
}

// e2e is sb-analysis on real Postgres and Redpanda, with the fake provider
// on the real-time lane.
type e2e struct {
	app     *app.App
	cfg     *config.Config
	prod    *kafka.Producer
	brokers []string
}

func newE2E(t *testing.T, ctx context.Context, inputs []string) *e2e {
	t.Helper()
	db := testenv.Postgres(t)
	brokers := testenv.Kafka(t)
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Kafka.TopicPrefix = testenv.TopicPrefix()
	cfg.Analysis.Inputs = inputs
	for name, r := range cfg.Analysis.Routes {
		r.Provider, r.Lane = "fake", "realtime"
		cfg.Analysis.Routes[name] = r
	}
	cfg.Analysis.Realtime.Wait, cfg.Analysis.Realtime.Poll = 0, 100*time.Millisecond
	cfg.Analysis.ConsumerGroup = fmt.Sprintf("sb-analysis-%d", rand.Int64N(1<<30))
	kc := kafka.ClientConfig{Brokers: brokers, ClientID: "e2e", TopicPrefix: cfg.Kafka.TopicPrefix}
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
	t.Cleanup(prod.Close)
	return &e2e{app: a, cfg: cfg, prod: prod, brokers: brokers}
}

func count(t *testing.T, a *app.App, q string) int {
	t.Helper()
	var n int
	if err := a.DB.QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func waitFor(t *testing.T, ctx context.Context, cond func() bool) {
	t.Helper()
	for !cond() {
		select {
		case <-ctx.Done():
			t.Fatal("timed out")
		case <-time.After(200 * time.Millisecond):
		}
	}
}
