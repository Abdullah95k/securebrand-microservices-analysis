package app_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	analysisv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/analysis/v1"
	fbv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/fb/v1"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/app"
	"github.com/abdullah95k/securebrand-microservices/internal/config"
	"github.com/abdullah95k/securebrand-microservices/internal/platform/kafka"
	"github.com/abdullah95k/securebrand-microservices/internal/testenv"
	"github.com/abdullah95k/securebrand-microservices/internal/topics"
)

// TestFacebookToAnalysisEndToEnd runs ingest, pipeline (fake provider) and
// relay against real Postgres and Redpanda: Facebook records in, analyses
// in Postgres and on analysis.result.v1, then an author erasure.
func TestFacebookToAnalysisEndToEnd(t *testing.T) {
	db := testenv.Postgres(t)
	brokers := testenv.Kafka(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Kafka.TopicPrefix = testenv.TopicPrefix()
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
	defer prod.Close()
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

	fns, err := a.Components([]string{"ingest", "pipeline", "relay"})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for _, f := range fns {
		wg.Add(1)
		go func() { defer wg.Done(); _ = f(runCtx) }()
	}
	defer func() { stop(); wg.Wait() }()

	waitFor(t, ctx, func() bool {
		return count(t, a, `SELECT count(*) FROM analysis.analyses WHERE is_current`) == 3 &&
			count(t, a, `SELECT count(*) FROM analysis.outbox`) == 0
	})
	// The results are on Kafka.
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ConsumeTopics(cfg.Kafka.TopicPrefix+topics.AnalysisResult),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	got := map[string]*analysisv1.ItemAnalysis{}
	for len(got) < 3 && ctx.Err() == nil {
		f := cl.PollFetches(ctx)
		f.EachRecord(func(r *kgo.Record) {
			var m analysisv1.ItemAnalysis
			if err := proto.Unmarshal(r.Value, &m); err != nil {
				t.Errorf("undecodable result: %v", err)
				return
			}
			if kafka.HeaderValue(r, kafka.HeaderType) != "securebrand.analysis.v1.ItemAnalysis" {
				t.Errorf("type header %q", kafka.HeaderValue(r, kafka.HeaderType))
			}
			got[m.ContentId] = &m
		})
	}
	c := got["fb:comment:11"]
	if c == nil || c.Sentiment.GetLabel() != "negative" || c.Provenance.GetModel() == "" || c.Provenance.GetAnalysisVersion() != 1 ||
		len(c.Entities) != 1 || c.Entities[0].EntityId != "asiacell" || c.SourceId != "fb:page:9" {
		t.Fatalf("result for comment 11: %v", c)
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
