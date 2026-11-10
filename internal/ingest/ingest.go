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

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/adapter"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/ametrics"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/kafka"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
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
	// gate pauses the content topics (paused) while the queue is full.
	gate    *Gate
	content []string
	paused  bool
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
	// Gate, when set, pauses the content topics while the analysis queue is
	// full; media announcements and erasures keep flowing.
	Gate *Gate
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
	content := make([]string, 0, len(cfg.Decoder.ContentTopics()))
	for _, t := range cfg.Decoder.ContentTopics() {
		content = append(content, cfg.Kafka.TopicPrefix+t)
	}
	return &Consumer{cl: cl, prefix: cfg.Kafka.TopicPrefix, dec: cfg.Decoder, store: cfg.Store, plan: cfg.Plan,
		consumer: cfg.Consumer, media: cfg.Media, maxBatch: cfg.MaxBatch, linger: cfg.Linger,
		gate: cfg.Gate, content: content}, nil
}

// Run consumes until ctx ends.
func (c *Consumer) Run(ctx context.Context) error {
	defer c.cl.Close()
	for {
		c.backpressure(ctx)
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

// backpressure pauses the content topics while the gate says the queue is
// full, and resumes them once it has room. Paused records stay in Kafka.
func (c *Consumer) backpressure(ctx context.Context) {
	if c.gate == nil {
		return
	}
	full := c.gate.Full(ctx)
	if full == c.paused {
		return
	}
	c.paused = full
	if full {
		c.cl.PauseFetchTopics(c.content...)
		ametrics.IngestPaused.Set(1)
		slog.Warn("ingest: analysis queue full; new content waits in Kafka", "max", c.gate.Max, "resume_at", c.gate.Resume)
		return
	}
	c.cl.ResumeFetchTopics(c.content...)
	ametrics.IngestPaused.Set(0)
	slog.Info("ingest: analysis queue has room again; content resumes")
}

// Flush stores one batch: items first, then media jobs, then erasures, so
// an erasure in the same batch also removes the items before it.
func (c *Consumer) Flush(ctx context.Context, recs []*kgo.Record) error {
	var (
		items    []*core.Item
		media    []*core.MediaJob
		gone     []*core.MediaGone
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
			case e.MediaGone != nil:
				gone = append(gone, e.MediaGone)
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
		// An announcement without a media ID, bucket or key can't be
		// fetched: InsertMediaJobs skips it. Count it, so a collector that
		// leaves the location out is seen, not silently dropped.
		unlocated := 0
		for _, j := range media {
			if j.MediaID == "" || j.Bucket == "" || j.Key == "" {
				unlocated++
				ametrics.Media.WithLabelValues(j.Kind, "no_location").Inc()
			}
		}
		if unlocated > 0 {
			slog.Warn("ingest: media announcements without a file location skipped", "count", unlocated, "of", len(media))
		}
		if _, err := c.store.InsertMediaJobs(ctx, media, c.consumer); err != nil {
			return fmt.Errorf("media: %w", err)
		}
	}
	// After the announcements: a file announced and deleted in one batch
	// isn't waited for.
	if len(gone) > 0 {
		n, err := c.store.MediaGone(ctx, gone)
		if err != nil {
			return fmt.Errorf("media deleted: %w", err)
		}
		if n > 0 {
			slog.Info("analysis: media files deleted before analysis", "jobs", n)
		}
	}
	for _, e := range erasures {
		stats, err := c.store.Erase(ctx, *e, c.consumer)
		if err != nil {
			return fmt.Errorf("erasure %s: %w", e.RequestID, err)
		}
		ametrics.Erasures.WithLabelValues(e.Subject).Inc()
		slog.Info("analysis: erasure event", "request", e.RequestID, "subject", e.Subject, "stats", stats)
	}
	return nil
}
