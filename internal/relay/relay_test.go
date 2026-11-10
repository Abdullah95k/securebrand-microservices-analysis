package relay_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/relay"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/testenv"
)

// refusing is a publisher whose Kafka refuses one topic.
type refusing struct {
	topic string
	sent  []string
}

func (p *refusing) Topic(base string) string { return base }

func (p *refusing) ProduceEach(_ context.Context, recs ...*kgo.Record) []error {
	errs := make([]error, len(recs))
	for i, r := range recs {
		if r.Topic == p.topic {
			errs[i] = errors.New("TOPIC_AUTHORIZATION_FAILED: not authorized to access topic")
			continue
		}
		p.sent = append(p.sent, r.Topic)
	}
	return errs
}

// The production failure of 10 November 2026: acknowledgements to a topic
// the Kafka user may not write sat at the head of the outbox, and nothing
// behind them went out. Now the refused records wait and everything else
// is published; the error names the topic.
func TestRefusedTopicDoesNotHoldBackTheRest(t *testing.T) {
	db := testenv.Postgres(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := store.New(db, time.UTC)
	// Acks first (the oldest), then a result, a media analysis and an
	// author flag behind them.
	for _, topic := range []string{"content.media.processed.v1", "content.media.processed.v1",
		"analysis.result.v1", "analysis.media.v1", "analysis.author.v1"} {
		if _, err := db.Exec(ctx, `INSERT INTO analysis.outbox (topic, key, type, payload) VALUES ($1, 'k', 'securebrand.x', '\x01')`, topic); err != nil {
			t.Fatal(err)
		}
	}
	p := &refusing{topic: "content.media.processed.v1"}
	r := &relay.Relay{Store: s, Producer: p}
	n, err := r.Once(ctx, 500)
	if n != 3 || strings.Join(p.sent, ",") != "analysis.result.v1,analysis.media.v1,analysis.author.v1" {
		t.Fatalf("published %d (%v), want the 3 records behind the refused ones", n, p.sent)
	}
	if err == nil || !strings.Contains(err.Error(), "content.media.processed.v1=2") {
		t.Fatalf("error %v doesn't name the refused topic", err)
	}
	var left int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM analysis.outbox WHERE topic = 'content.media.processed.v1' AND attempts = 1`).Scan(&left); err != nil || left != 2 {
		t.Fatalf("%d refused records kept, want 2 (%v)", left, err)
	}
}
