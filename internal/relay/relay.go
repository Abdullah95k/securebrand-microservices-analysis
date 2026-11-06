// Package relay publishes the analysis outbox (results, media analyses,
// media acknowledgements) to Kafka after the transactions that wrote them
// commit. Delivery is at least once; event ids let consumers deduplicate.
package relay

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	contracts "github.com/abdullah95k/securebrand-contracts"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/ametrics"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/kafka"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
)

// Relay drains the outbox.
type Relay struct {
	Store    *store.Store
	Producer *kafka.Producer
	Batch    int
	Idle     time.Duration
}

// Run publishes until ctx ends.
func (r *Relay) Run(ctx context.Context) error {
	batch := r.Batch
	if batch <= 0 {
		batch = 500
	}
	idle := r.Idle
	if idle <= 0 {
		idle = 500 * time.Millisecond
	}
	for ctx.Err() == nil {
		n, err := r.Once(ctx, batch)
		if err != nil && ctx.Err() == nil {
			slog.Warn("analysis: outbox relay", "err", err)
		}
		if n < batch {
			select {
			case <-ctx.Done():
			case <-time.After(idle):
			}
		}
	}
	return nil
}

// Once publishes up to batch records.
func (r *Relay) Once(ctx context.Context, batch int) (int, error) {
	n, err := r.Store.DrainOutbox(ctx, batch, func(ctx context.Context, recs []store.OutboxRecord) error {
		out := make([]*kgo.Record, len(recs))
		for i, rec := range recs {
			out[i] = &kgo.Record{Topic: r.Producer.Topic(rec.Topic), Key: []byte(rec.Key), Value: rec.Payload,
				Headers: Headers(rec.Type)}
		}
		if err := r.Producer.ProduceSync(ctx, out...); err != nil {
			return err
		}
		for _, rec := range recs {
			ametrics.Published.WithLabelValues(rec.Topic).Inc()
		}
		return nil
	})
	if backlog, berr := r.Store.OutboxBacklog(ctx); berr == nil {
		ametrics.OutboxBacklog.Set(float64(backlog))
	}
	return n, err
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
