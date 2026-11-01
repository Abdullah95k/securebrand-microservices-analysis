package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/ametrics"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/provider"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/store"
)

// RunRealtime runs the real-time lane until ctx ends: each worker claims a
// route's due items, groups them by thread and analyses the groups.
func (p *Pipeline) RunRealtime(ctx context.Context) error {
	workers := max(p.Cfg.Realtime.Workers, 1)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				n, err := p.RealtimeOnce(ctx)
				if err != nil && ctx.Err() == nil {
					slog.Warn("analysis: realtime lane", "err", err)
				}
				if n == 0 || err != nil {
					sleep(ctx, p.Cfg.Realtime.Poll)
				}
			}
		}()
	}
	wg.Wait()
	return nil
}

// RealtimeOnce claims and analyses one round of real-time items, all
// routes; it returns the items processed.
func (p *Pipeline) RealtimeOnce(ctx context.Context) (int, error) {
	total := 0
	for _, route := range routeOrder {
		if rc, ok := p.Cfg.Routes[route]; !ok || !rc.On() {
			continue
		}
		claimed, err := p.Store.Claim(ctx, core.LaneRealtime, route, p.Cfg.Grouping.MaxItems*2, p.Cfg.Realtime.Lease, p.Cfg.Realtime.Wait)
		if err != nil {
			return total, err
		}
		var errs []error
		for _, g := range p.Groups(route, claimed) {
			if err := p.RunGroup(ctx, g, core.LaneRealtime); err != nil {
				errs = append(errs, err)
			}
			total += len(g.items)
		}
		if err := errors.Join(errs...); err != nil {
			return total, err
		}
	}
	return total, nil
}

// RunBatch submits batch-lane items every batch.submit_every and polls
// open batches every batch.poll_every until ctx ends.
func (p *Pipeline) RunBatch(ctx context.Context) error {
	submit := time.NewTicker(p.Cfg.Batch.SubmitEvery)
	poll := time.NewTicker(p.Cfg.Batch.PollEvery)
	defer submit.Stop()
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-submit.C:
			if err := p.SubmitBatches(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("analysis: batch submit", "err", err)
			}
		case <-poll.C:
			if err := p.PollBatches(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("analysis: batch poll", "err", err)
			}
		}
	}
}

// SubmitBatches claims batch-lane items per route and sends them through
// the provider's batch API; a provider without one gets them in real time.
func (p *Pipeline) SubmitBatches(ctx context.Context) error {
	var errs []error
	for _, route := range routeOrder {
		rc, ok := p.Cfg.Routes[route]
		if !ok || !rc.On() {
			continue
		}
		if err := p.submitRoute(ctx, route, rc.Provider, rc.Model); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (p *Pipeline) submitRoute(ctx context.Context, route, prov, model string) error {
	batcher, hasBatch := p.Providers.Batcher(prov)
	claimed, err := p.Store.Claim(ctx, core.LaneBatch, route, p.Cfg.Batch.MaxItems, p.Cfg.Batch.Lease, 0)
	if err != nil || len(claimed) == 0 {
		return err
	}
	groups := p.Groups(route, claimed)
	realtime := func() error {
		var errs []error
		for _, g := range groups {
			if err := p.RunGroup(ctx, g, core.LaneBatch); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	if !hasBatch {
		// No batch API configured: analyse in real time.
		return realtime()
	}
	// While the primary's batch API keeps failing, submissions are still
	// retried (each round) unless its circuit is open. Mixing models in bulk
	// data hurts comparability, so only after batch.fallback_after does the
	// backlog go to the route's fallback in real time.
	since, down := p.downSince(prov)
	useFallback := down && p.now().Sub(since) >= p.Cfg.Batch.FallbackAfter && p.Cfg.Routes[route].FallbackProvider != ""
	if !p.Providers.Available(prov) {
		if useFallback {
			slog.Warn("analysis: batch provider down; using the fallback", "route", route, "provider", prov, "since", since)
			return realtime()
		}
		p.markDown(prov)
		return p.Store.ReleaseUnsent(ctx, ids(flatten(groups)), route, 5*time.Minute)
	}

	type pending struct {
		row store.RequestRow
		req provider.Request
		g   group
	}
	var reqs []pending
	for _, g := range groups {
		b, err := p.build(ctx, g, model)
		if err != nil {
			return err
		}
		id := uuid.New()
		b.req.ID = id.String()
		est := p.estimate(b, true)
		ok, err := p.Budget.Allow(ctx, route, est, false)
		if err != nil {
			return err
		}
		if !ok {
			if err := p.Store.ReleaseUnsent(ctx, ids(g.items), route, 10*time.Minute); err != nil {
				return err
			}
			continue
		}
		p.Budget.Add(route, est)
		row := store.RequestRow{ID: id, Route: route, Lane: core.LaneBatch, Provider: prov, Model: model,
			ContentIDs: ids(b.items), InputHashes: b.hashes, EstCostUSD: est}
		if err := p.Store.StartRequest(ctx, row); err != nil {
			return err
		}
		reqs = append(reqs, pending{row: row, req: b.req, g: g})
	}
	size := max(p.Cfg.Batch.MaxRequests, 1)
	for start := 0; start < len(reqs); start += size {
		chunk := reqs[start:min(start+size, len(reqs))]
		batch := make([]provider.Request, len(chunk))
		reqIDs := make([]uuid.UUID, len(chunk))
		for i, r := range chunk {
			batch[i], reqIDs[i] = r.req, r.row.ID
		}
		id, err := batcher.SubmitBatch(ctx, batch)
		p.Providers.Observe(prov, err)
		if err != nil && provider.Retryable(err) {
			p.markDown(prov)
		}
		if err != nil {
			ametrics.Requests.WithLabelValues(prov, model, core.LaneBatch, "submit_error").Add(float64(len(chunk)))
			// Abort this chunk and every later one (none was sent).
			var unsent []uuid.UUID
			for _, r := range reqs[start:] {
				unsent = append(unsent, r.row.ID)
			}
			if aerr := p.Store.AbortRequests(context.WithoutCancel(ctx), unsent, "batch submit: "+err.Error(), 5*time.Minute); aerr != nil {
				return errors.Join(err, aerr)
			}
			if !useFallback {
				return err
			}
			// Down for longer than batch.fallback_after: the rest goes to
			// the fallback in real time (the aborted items stay unclaimable
			// for 5 minutes meanwhile).
			slog.Warn("analysis: batch provider down; using the fallback", "route", route, "provider", prov, "since", since, "err", err)
			var errs []error
			for _, r := range reqs[start:] {
				if rerr := p.RunGroup(ctx, r.g, core.LaneBatch); rerr != nil {
					errs = append(errs, rerr)
				}
			}
			return errors.Join(errs...)
		}
		p.markUp(prov)
		if err := p.Store.SetBatchID(ctx, id, prov, model, route, reqIDs); err != nil {
			return err
		}
		ametrics.Requests.WithLabelValues(prov, model, core.LaneBatch, "submitted").Add(float64(len(chunk)))
		slog.Info("analysis: batch submitted", "route", route, "model", model, "batch", id, "requests", len(chunk))
	}
	return nil
}

// PollBatches collects the results of finished batches.
func (p *Pipeline) PollBatches(ctx context.Context) error {
	open, err := p.Store.ClaimOpenBatches(ctx, 10, 5*time.Minute)
	if err != nil {
		return err
	}
	ametrics.OpenBatches.Set(float64(len(open)))
	var errs []error
	for _, b := range open {
		if err := p.collect(ctx, b); err != nil {
			errs = append(errs, err)
			_ = p.Store.ReleaseBatch(context.WithoutCancel(ctx), b.ID)
		}
	}
	return errors.Join(errs...)
}

func (p *Pipeline) collect(ctx context.Context, b store.OpenBatch) error {
	batcher, ok := p.Providers.Batcher(b.Provider)
	if !ok {
		return p.Store.EndBatch(ctx, b.ID, "failed", "provider "+b.Provider+" has no batch API configured")
	}
	done, err := batcher.BatchDone(ctx, b.ID)
	if err != nil {
		return err
	}
	if !done {
		return p.Store.ReleaseBatch(ctx, b.ID)
	}
	results, err := batcher.BatchResults(ctx, b.ID)
	if err != nil {
		return err
	}
	for _, r := range results {
		if err := p.collectOne(ctx, r); err != nil {
			return err
		}
	}
	stale, err := p.Store.StaleRequests(ctx, b.ID)
	if err != nil {
		return err
	}
	for _, id := range stale {
		if err := p.Store.FailRequest(ctx, id, "no result in batch", p.Cfg.Limits.MaxAttempts); err != nil {
			return err
		}
	}
	slog.Info("analysis: batch collected", "batch", b.ID, "results", len(results))
	return p.Store.EndBatch(ctx, b.ID, "ended", "")
}

func (p *Pipeline) collectOne(ctx context.Context, r provider.BatchResult) error {
	id, err := uuid.Parse(r.ID)
	if err != nil {
		slog.Warn("analysis: batch result with a foreign id", "id", r.ID)
		return nil
	}
	req, err := p.Store.Request(ctx, id)
	if err != nil {
		return err
	}
	if req.Status != "submitted" {
		return nil // already handled (a re-poll after a crash)
	}
	if r.Err != nil {
		ametrics.Requests.WithLabelValues(req.Provider, req.Model, core.LaneBatch, "error").Inc()
		return p.Store.FailRequest(ctx, id, r.Err.Error(), p.Cfg.Limits.MaxAttempts)
	}
	byID, err := p.Store.ItemsByID(ctx, req.ContentIDs)
	if err != nil {
		return err
	}
	// Prompt order is kept; an item erased meanwhile becomes a placeholder
	// whose result is dropped (no index row).
	items := make([]core.Item, len(req.ContentIDs))
	for i, cid := range req.ContentIDs {
		if it, ok := byID[cid]; ok {
			items[i] = it
		} else {
			items[i] = core.Item{ContentID: cid}
		}
	}
	ametrics.Requests.WithLabelValues(req.Provider, req.Model, core.LaneBatch, "ok").Inc()
	s := sent{id: id, route: req.Route, lane: core.LaneBatch, provider: req.Provider, model: req.Model, fallback: req.Fallback}
	return p.handle(ctx, s, items, req.InputHashes, r.Response)
}

func sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		d = time.Second
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (p *Pipeline) downSince(prov string) (time.Time, bool) {
	p.downMu.Lock()
	defer p.downMu.Unlock()
	t, ok := p.down[prov]
	return t, ok
}

func (p *Pipeline) markDown(prov string) {
	p.downMu.Lock()
	defer p.downMu.Unlock()
	if _, ok := p.down[prov]; !ok {
		p.down[prov] = p.now()
	}
}

func (p *Pipeline) markUp(prov string) {
	p.downMu.Lock()
	defer p.downMu.Unlock()
	delete(p.down, prov)
}

func flatten(gs []group) []core.Item {
	var out []core.Item
	for _, g := range gs {
		out = append(out, g.items...)
	}
	return out
}
