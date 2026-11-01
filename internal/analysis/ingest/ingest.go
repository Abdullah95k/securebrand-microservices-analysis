// Package ingest reads the input topics in batches and writes them to the
// analysis store: content items (queued for analysis), media announcements
// (media jobs) and erasures. Offsets are committed only after the batch is
// stored, so a crash replays at most one batch, which the store's
// idempotency absorbs.
package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/adapter"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/ametrics"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/store"
	"github.com/abdullah95k/securebrand-microservices/internal/platform/kafka"
)

// Consumer is the ingest consumer.
type Consumer struct {
	cl       *kgo.Client
	prefix   string
	dec      adapter.Decoder
	store    *store.Store
	plan     store.Planner
	consumer string // media ack consumer name
	media    bool
	maxBatch int
	linger   time.Duration
}

// Config configures the consumer.
type Config struct {
	Kafka    kafka.ClientConfig
	Group    string
	Decoder  adapter.Decoder
	Store    *store.Store
	Plan     store.Planner
	Media    bool   // consume media announcements
	Consumer string // media ack consumer name
	MaxBatch int
	Linger   time.Duration
}

// New creates the consumer over the decoder's topics.
func New(cfg Config) (*Consumer, error) {
	base := cfg.Decoder.ItemTopics()
	if cfg.Media {
		base = append(base, cfg.Decoder.MediaTopics()...)
	}
	if len(base) == 0 {
		return nil, errors.New("ingest: no input topics (analysis.inputs)")
	}
	ts := make([]string, len(base))
	for i, t := range base {
		ts[i] = cfg.Kafka.TopicPrefix + t
	}
	cl, err := kgo.NewClient(append(cfg.Kafka.Opts(),
		kgo.ConsumerGroup(cfg.Kafka.TopicPrefix+cfg.Group),
		kgo.ConsumeTopics(ts...),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
		kgo.Balancers(kgo.CooperativeStickyBalancer()),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.FetchMaxWait(500*time.Millisecond),
	)...)
	if err != nil {
		return nil, fmt.Errorf("ingest: %w", err)
	}
	if cfg.MaxBatch <= 0 {
		cfg.MaxBatch = 2000
	}
	if cfg.Linger <= 0 {
		cfg.Linger = 500 * time.Millisecond
	}
	return &Consumer{cl: cl, prefix: cfg.Kafka.TopicPrefix, dec: cfg.Decoder, store: cfg.Store, plan: cfg.Plan,
		consumer: cfg.Consumer, media: cfg.Media, maxBatch: cfg.MaxBatch, linger: cfg.Linger}, nil
}

// Run consumes until ctx ends.
func (c *Consumer) Run(ctx context.Context) error {
	defer c.cl.Close()
	for {
		var recs []*kgo.Record
		deadline := time.Now().Add(c.linger)
		for len(recs) < c.maxBatch {
			pctx, cancel := context.WithDeadline(ctx, deadline)
			f := c.cl.PollRecords(pctx, c.maxBatch-len(recs))
			cancel()
			if ctx.Err() != nil {
				c.cl.AllowRebalance()
				return nil
			}
			f.EachError(func(t string, p int32, err error) {
				if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
					slog.Warn("ingest: fetch error", "topic", t, "partition", p, "err", err)
				}
			})
			recs = append(recs, f.Records()...)
			if time.Now().After(deadline) || (len(recs) > 0 && f.NumRecords() == 0) {
				break
			}
		}
		if len(recs) == 0 {
			c.cl.AllowRebalance()
			continue
		}
		for {
			err := c.Flush(ctx, recs)
			if err == nil {
				break
			}
			if ctx.Err() != nil {
				c.cl.AllowRebalance()
				return nil
			}
			// Never commit past unstored records: retry the same batch.
			slog.Error("ingest: batch failed; retrying", "records", len(recs), "err", err)
			select {
			case <-ctx.Done():
				c.cl.AllowRebalance()
				return nil
			case <-time.After(2 * time.Second):
			}
		}
		if err := c.cl.CommitRecords(ctx, recs...); err != nil {
			slog.Warn("ingest: commit failed", "err", err)
		}
		c.cl.AllowRebalance()
	}
}

// Flush stores one batch: items first, then media jobs, then erasures, so
// an erasure in the same batch also removes the items before it.
func (c *Consumer) Flush(ctx context.Context, recs []*kgo.Record) error {
	var (
		items    []*core.Item
		media    []*core.MediaJob
		erasures []*core.Erasure
	)
	for _, r := range recs {
		topic := strings.TrimPrefix(r.Topic, c.prefix)
		evs, err := c.dec.Decode(topic, r.Value)
		if err != nil {
			// Logged with position only: a payload holds personal data.
			slog.Error("ingest: undecodable record skipped", "topic", r.Topic, "partition", r.Partition, "offset", r.Offset, "err", err)
			ametrics.Undecodable.WithLabelValues(topic).Inc()
			continue
		}
		for _, e := range evs {
			switch {
			case e.Item != nil:
				items = append(items, e.Item)
			case e.Media != nil:
				media = append(media, e.Media)
			case e.Erasure != nil:
				erasures = append(erasures, e.Erasure)
			}
		}
	}
	if len(items) > 0 {
		st, err := c.store.Ingest(ctx, items, c.plan)
		if err != nil {
			return fmt.Errorf("items: %w", err)
		}
		platform := items[0].Platform
		for k, v := range map[string]int{"new": st.New, "edited": st.Edited, "unchanged": st.Unchanged, "skipped": st.Skipped,
			"deleted": st.Deleted, "redacted": st.Redacted, "suppressed": st.Suppressed} {
			if v > 0 {
				ametrics.Ingested.WithLabelValues(platform, k).Add(float64(v))
			}
		}
	}
	if len(media) > 0 {
		if _, err := c.store.InsertMediaJobs(ctx, media, c.consumer); err != nil {
			return fmt.Errorf("media: %w", err)
		}
	}
	for _, e := range erasures {
		stats, err := c.store.Erase(ctx, *e, c.consumer)
		if err != nil {
			return fmt.Errorf("erasure %s: %w", e.RequestID, err)
		}
		ametrics.Erasures.WithLabelValues(e.Subject).Inc()
		slog.Info("analysis: erasure done", "request", e.RequestID, "subject", e.Subject, "stats", stats)
	}
	return nil
}
