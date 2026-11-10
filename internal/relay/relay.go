// Package relay publishes the analysis outbox (results, media analyses,
// media acknowledgements) to Kafka after the transactions that wrote them
// commit. Delivery is at least once; event ids let consumers deduplicate.
package relay

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	contracts "github.com/abdullah95k/securebrand-contracts"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/ametrics"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/kafka"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/run"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
)

// Publisher produces records one by one: *kafka.Producer.
type Publisher interface {
	Topic(base string) string
	ProduceEach(ctx context.Context, recs ...*kgo.Record) []error
}

// Relay drains the outbox.
type Relay struct {
	Store    *store.Store
	Producer Publisher
	Batch    int
	// Idle is the wait after a poll that found the outbox empty (500 ms by
	// default); while it stays empty the wait doubles up to IdleMax (10 s
	// by default): the database is shared, and an idle outbox mustn't be
	// queried twice a second.
	Idle    time.Duration
	IdleMax time.Duration

	backlogAt time.Time
}

// Run publishes until ctx ends.
func (r *Relay) Run(ctx context.Context) error {
	batch := r.Batch
	if batch <= 0 {
		batch = 500
	}
	idle := &run.Idle{Min: r.Idle, Max: r.IdleMax}
	if idle.Min <= 0 {
		idle.Min = 500 * time.Millisecond
	}
	if idle.Max <= 0 {
		idle.Max = 10 * time.Second
	}
	for ctx.Err() == nil {
		n, err := r.Once(ctx, batch)
		if err != nil && ctx.Err() == nil {
			slog.Warn("analysis: outbox relay", "err", err)
		}
		switch {
		case n >= batch && err == nil:
			// A full batch: more is waiting.
			idle.Busy()
		case n > 0:
			// The rest went: the next poll comes soon.
			idle.Busy()
			idle.Wait(ctx)
		default:
			idle.Wait(ctx)
		}
	}
	return nil
}

// backlogEvery is how often an empty outbox's backlog gauge is refreshed;
// after a drain that published something it is refreshed at once.
const backlogEvery = time.Minute

// Once publishes up to batch records that are due. A record Kafka refuses
// or doesn't take (its topic not writable, for example) waits and is tried
// again later (store.DrainOutbox); the others go out. It returns how many
// went out, and an error naming the topics that refused records.
func (r *Relay) Once(ctx context.Context, batch int) (int, error) {
	refused := map[string]int{}
	var first error
	n, failed, err := r.Store.DrainOutbox(ctx, batch, func(ctx context.Context, recs []store.OutboxRecord) []error {
		out := make([]*kgo.Record, len(recs))
		for i, rec := range recs {
			out[i] = &kgo.Record{Topic: r.Producer.Topic(rec.Topic), Key: []byte(rec.Key), Value: rec.Payload,
				Headers: Headers(rec.Type)}
		}
		errs := r.Producer.ProduceEach(ctx, out...)
		for i, rec := range recs {
			if errs[i] != nil {
				ametrics.PublishFailed.WithLabelValues(rec.Topic).Inc()
				refused[rec.Topic]++
				if first == nil {
					first = errs[i]
				}
				continue
			}
			ametrics.Published.WithLabelValues(rec.Topic).Inc()
		}
		return errs
	})
	if err == nil && failed > 0 {
		err = fmt.Errorf("%d records not published, tried again later (%s): %w", failed, topicCounts(refused), first)
	}
	if n > 0 || failed > 0 || err != nil || time.Since(r.backlogAt) >= backlogEvery {
		if backlog, berr := r.Store.OutboxBacklog(ctx); berr == nil {
			ametrics.OutboxBacklog.Set(float64(backlog))
			r.backlogAt = time.Now()
		}
		if held, herr := r.Store.OutboxHeld(ctx); herr == nil {
			ametrics.OutboxHeld.Set(float64(held))
		}
	}
	return n, err
}

// topicCounts formats refusals by topic, in topic order: "a=2, b=1".
func topicCounts(m map[string]int) string {
	names := make([]string, 0, len(m))
	for t := range m {
		names = append(names, t)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, t := range names {
		parts[i] = fmt.Sprintf("%s=%d", t, m[t])
	}
	return strings.Join(parts, ", ")
}

// Headers are a record's headers by its message type: the contract's
// "sb-type" (the full message name) and "sb-contract" (the contract version)
// on contract messages; fbs's "fbs-type" on fbs's own (the media
// acknowledgements of fb.media.processed.v1).
func Headers(msgType string) []kgo.RecordHeader {
	if strings.HasPrefix(msgType, "securebrand.fb.") {
		return []kgo.RecordHeader{
			{Key: kafka.HeaderType, Value: []byte(msgType)},
			{Key: kafka.HeaderContentType, Value: []byte(kafka.ContentTypeProto)},
		}
	}
	return []kgo.RecordHeader{
		{Key: HeaderSBType, Value: []byte(msgType)},
		{Key: HeaderSBContract, Value: []byte(contracts.Version)},
		{Key: kafka.HeaderContentType, Value: []byte(kafka.ContentTypeProto)},
	}
}

// The contract's record headers.
const (
	HeaderSBType     = "sb-type"
	HeaderSBContract = "sb-contract"
)
