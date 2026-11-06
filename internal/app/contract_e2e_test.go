package app_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	contracts "github.com/abdullah95k/securebrand-contracts"
	"github.com/abdullah95k/securebrand-contracts/conformance"
	analysisv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/analysis/v1"
	contentv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/content/v1"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/kafka"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/relay"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/topics"
)

// fakePlatform publishes the contract's content events the way the
// platform's collectors do: plain protobuf with the sb-type and sb-contract
// headers, keyed by post id.
type fakePlatform struct {
	t    *testing.T
	prod *kafka.Producer
}

func (f fakePlatform) send(ctx context.Context, topic, key string, m proto.Message) {
	f.t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		f.t.Fatal(err)
	}
	rec := &kgo.Record{Topic: f.prod.Topic(topic), Key: []byte(key), Value: b, Headers: relay.Headers(string(proto.MessageName(m)))}
	if err := f.prod.ProduceSync(ctx, rec); err != nil {
		f.t.Fatal(err)
	}
}

func envelope(kind, id, native string, at time.Time, version int64) *contentv1.Envelope {
	return &contentv1.Envelope{EventId: fmt.Sprintf("evt-%s-%d", id, version), Platform: "fb", Kind: kind, Id: id, NativeId: native,
		ObservedAt: timestamppb.New(at), Version: version,
		Provenance: &contentv1.Provenance{Collector: "fbs", CollectorVersion: "test", ParserVersion: "1",
			Acquisition: contentv1.Acquisition_ACQUISITION_SCRAPED_LOGGED_OUT}}
}

func published(at time.Time) *contentv1.Time {
	return &contentv1.Time{At: timestamppb.New(at), Precision: contentv1.TimePrecision_TIME_PRECISION_EXACT}
}

// TestContractToAnalysisEndToEnd runs ingest, pipeline (fake provider) and
// relay against real Postgres and Redpanda with the platform's contract as
// input: a fake platform publishes a post, a comment, a reply and a comment
// by a hidden author, then a partial update and a partial deletion. Every
// result on analysis.result.v1 must pass the contract's conformance checks
// in production mode and carry the contract's headers.
func TestContractToAnalysisEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	e := newE2E(t, ctx, []string{"content"})
	a := e.app
	pf := fakePlatform{t: t, prod: e.prod}
	// A client tracks Asiacell: a comment naming it is routed as urgent.
	if _, err := a.DB.Exec(ctx, `INSERT INTO analysis.clients (client_id, name) VALUES ('c1', 'Client')`); err != nil {
		t.Fatal(err)
	}
	if err := a.Store.TrackEntity(ctx, "c1", "asiacell", "own"); err != nil {
		t.Fatal(err)
	}
	if err := a.RefreshEntities(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	v := now.UnixMicro()
	account := &contentv1.AccountRef{Id: "fb:account:9", Handle: "asiacell", DisplayName: "Asiacell"}
	text := func(s string) *contentv1.Text { return &contentv1.Text{Text: s, PlatformLanguage: "ar"} }
	visible := &contentv1.Status{Availability: contentv1.Availability_AVAILABILITY_VISIBLE}
	comment := func(id, native string, depth int32, parent string, author *contentv1.Author, body string) *contentv1.Comment {
		return &contentv1.Comment{Envelope: envelope("comment", id, native, now, v), PostId: "fb:post:1", AccountId: "fb:account:9",
			ParentCommentId: parent, Depth: depth, Author: author, Text: text(body), PublishedAt: published(now), Status: visible}
	}
	user := func(id string) *contentv1.Author { return &contentv1.Author{Account: &contentv1.AccountRef{Id: id}} }

	pf.send(ctx, topics.ContentPost, "fb:post:1", &contentv1.Post{Envelope: envelope("post", "fb:post:1", "1", now, v),
		AccountId: "fb:account:9", Account: account, Text: text("عرض جديد"), PublishedAt: published(now), Status: visible})
	pf.send(ctx, topics.ContentComment, "fb:post:1", comment("fb:comment:11", "11", 0, "", user("fb:account:501"), "اسياسيل خايسة"))
	pf.send(ctx, topics.ContentComment, "fb:post:1", comment("fb:comment:12", "12", 1, "fb:comment:11", user("fb:account:502"), "شكرا"))
	pf.send(ctx, topics.ContentComment, "fb:post:1", comment("fb:comment:13", "13", 0, "",
		&contentv1.Author{Hidden: true}, "الخدمة زينة"))

	stop := e.run(t, ctx, "ingest", "pipeline", "relay")
	defer stop()

	waitFor(t, ctx, func() bool {
		return count(t, a, `SELECT count(*) FROM analysis.analyses WHERE is_current`) == 4 &&
			count(t, a, `SELECT count(*) FROM analysis.outbox WHERE topic = 'analysis.result.v1'`) == 0
	})

	got := consume(t, ctx, e.brokers, e.cfg.Kafka.TopicPrefix+topics.AnalysisResult, 4)
	for id, r := range got {
		var m analysisv1.ItemAnalysis
		if err := proto.Unmarshal(r.Value, &m); err != nil {
			t.Fatalf("%s: undecodable result: %v", id, err)
		}
		if err := conformance.ItemAnalysis(&m, conformance.Options{Production: true}); err != nil {
			t.Errorf("%s: %v", id, err)
		}
		if h := kafka.HeaderValue(r, relay.HeaderSBType); h != "securebrand.analysis.v1.ItemAnalysis" {
			t.Errorf("%s: sb-type %q", id, h)
		}
		if h := kafka.HeaderValue(r, relay.HeaderSBContract); h != contracts.Version {
			t.Errorf("%s: sb-contract %q, want %q", id, h, contracts.Version)
		}
		if string(r.Key) != m.GetContentId() {
			t.Errorf("%s: key %q, want the content id", id, r.Key)
		}
		if m.GetContentVersion() != v {
			t.Errorf("%s: content_version %d, want %d", id, m.GetContentVersion(), v)
		}
		if m.GetProvenance().GetProducer() != "sb-analysis" {
			t.Errorf("%s: producer %q", id, m.GetProvenance().GetProducer())
		}
	}
	res := func(id string) *analysisv1.ItemAnalysis {
		var m analysisv1.ItemAnalysis
		if r := got[id]; r != nil {
			_ = proto.Unmarshal(r.Value, &m)
		}
		return &m
	}
	if p := res("fb:post:1"); p.GetKind() != "post" || p.GetAccountId() != "fb:account:9" || p.GetAuthorId() != "fb:account:9" {
		t.Errorf("post: %v", p)
	}
	c := res("fb:comment:11")
	if c.GetKind() != "comment" || c.GetPostId() != "fb:post:1" || c.GetAuthorId() != "fb:account:501" ||
		c.GetSentiment().GetLabel() != "negative" || len(c.GetEntities()) != 1 || c.GetEntities()[0].GetEntityId() != "asiacell" {
		t.Errorf("comment 11: %v", c)
	}
	if c.GetRouting() != "tracked_entity" || len(c.GetMatchedEntityIds()) != 1 || c.GetMatchedEntityIds()[0] != "asiacell" {
		t.Errorf("comment 11 routing %q, matched %v", c.GetRouting(), c.GetMatchedEntityIds())
	}
	if r := res("fb:comment:12"); r.GetDepth() != 1 || r.GetParentCommentId() != "fb:comment:11" || r.GetAuthorId() != "fb:account:502" {
		t.Errorf("reply 12: %v", r)
	}
	if h := res("fb:comment:13"); h.GetAuthorId() != "" {
		t.Errorf("hidden author's comment carries author_id %q", h.GetAuthorId())
	}
	if n := count(t, a, `SELECT count(*) FROM analysis.content_items WHERE content_id = 'fb:comment:13' AND author_key IS NULL`); n != 1 {
		t.Error("a hidden author got an author key")
	}

	// A partial event that is not a deletion (counts, a field) is never an
	// edit: the text and the analysis stay.
	pf.send(ctx, topics.ContentPost, "fb:post:1", &contentv1.Post{Envelope: func() *contentv1.Envelope {
		env := envelope("post", "fb:post:1", "1", now.Add(time.Minute), v+1)
		env.Partial = true
		return env
	}(), AccountId: "fb:account:9", Status: visible})
	// A partial deletion marks the comment deleted and keeps what is stored.
	del := &contentv1.Comment{Envelope: envelope("comment", "fb:comment:11", "11", now.Add(2*time.Minute), v+2), PostId: "fb:post:1",
		AccountId: "fb:account:9", Status: &contentv1.Status{Availability: contentv1.Availability_AVAILABILITY_DELETED}}
	del.Envelope.Partial = true
	pf.send(ctx, topics.ContentComment, "fb:post:1", del)
	waitFor(t, ctx, func() bool {
		return count(t, a, `SELECT count(*) FROM analysis.content_items WHERE content_id = 'fb:comment:11' AND status = 'deleted'
			AND text IS NOT NULL`) == 1
	})
	if n := count(t, a, `SELECT count(*) FROM analysis.content_items WHERE content_id = 'fb:post:1' AND text = 'عرض جديد'
		AND edit_count = 0`); n != 1 {
		t.Error("a partial update changed the post")
	}
	if n := count(t, a, `SELECT count(*) FROM analysis.analyses WHERE is_current`); n != 4 {
		t.Errorf("%d current analyses after the partial events, want 4", n)
	}
}

// consume reads n records with distinct keys from topic, from the start.
func consume(t *testing.T, ctx context.Context, brokers []string, topic string, n int) map[string]*kgo.Record {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	got := map[string]*kgo.Record{}
	for len(got) < n && ctx.Err() == nil {
		cl.PollFetches(ctx).EachRecord(func(r *kgo.Record) { got[string(r.Key)] = r })
	}
	if len(got) < n {
		t.Fatalf("%d of %d records on %s", len(got), n, topic)
	}
	return got
}

// run starts the named components until the returned stop is called.
func (e *e2e) run(t *testing.T, ctx context.Context, names ...string) func() {
	t.Helper()
	fns, err := e.app.Components(names)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for _, f := range fns {
		wg.Add(1)
		go func() { defer wg.Done(); _ = f(runCtx) }()
	}
	return func() { stop(); wg.Wait() }
}
