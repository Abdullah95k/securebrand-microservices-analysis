package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/ametrics"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/run"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
)

// RunRealtime runs the real-time lane until ctx ends: each worker claims a
// route's due items, groups them by thread and analyses the groups. While
// the lane is empty one worker polls, every realtime.poll at first and
// then less often, up to realtime.idle_poll (run.Idle): the database is
// shared, and an idle poll every second per worker kept its connections
// busy.
func (p *Pipeline) RunRealtime(ctx context.Context) error {
	workers := max(p.Cfg.Realtime.Workers, 1)
	idle := &run.Idle{Min: p.Cfg.Realtime.Poll, Max: p.Cfg.Realtime.IdlePoll}
	if idle.Min <= 0 {
		idle.Min = time.Second
	}
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
				if n > 0 {
					idle.Busy()
					continue
				}
				idle.Wait(ctx)
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
	// A failing route must not hold the others back: every route gets its
	// round, errors are reported together.
	var errs []error
	for _, route := range routeOrder {
		if rc, ok := p.Cfg.Routes[route]; !ok || !rc.On() {
			continue
		}
		claimed, err := p.Store.Claim(ctx, core.LaneRealtime, route, p.Cfg.Grouping.MaxItems*2, p.Cfg.Realtime.Lease, p.Cfg.Realtime.Wait)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, g := range p.Groups(route, claimed) {
			if err := p.RunGroup(ctx, g, core.LaneRealtime); err != nil {
				errs = append(errs, err)
			}
			total += len(g.items)
		}
	}
	return total, errors.Join(errs...)
}

// RunBatch submits batch-lane items at start and then every
// batch.submit_every, and polls open batches every batch.poll_every, until
// ctx ends.
func (p *Pipeline) RunBatch(ctx context.Context) error {
	submit := time.NewTicker(p.Cfg.Batch.SubmitEvery)
	poll := time.NewTicker(p.Cfg.Batch.PollEvery)
	defer submit.Stop()
	defer poll.Stop()
	// The first round runs at start, not one submit_every later: a restart
	// then delays the queue by a round's build time only.
	if err := p.PollBatches(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("analysis: batch poll", "err", err)
	}
	if err := p.SubmitBatches(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("analysis: batch submit", "err", err)
	}
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
// First, work an earlier round claimed but never sent (the process stopped
// or failed in between) is given back.
func (p *Pipeline) SubmitBatches(ctx context.Context) error {
	if reqs, items, err := p.Store.ReleaseStrandedBatch(ctx, p.submitLease()); err != nil {
		slog.Warn("analysis: releasing stranded batch work", "err", err)
	} else if reqs > 0 || items > 0 {
		slog.Warn("analysis: released batch work that was claimed but never sent", "requests", reqs, "items", items)
	}
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

// topUp adds to a round's claim each claimed thread's other waiting
// comments, up to grouping.max_items a thread and batch.top_up_items in
// all: a claim of the oldest items holds one or two comments of most
// threads, and Groups packs only what was claimed. Posts and items retried
// after a fault of their own answer (sent alone) don't take part.
func (p *Pipeline) topUp(ctx context.Context, route string, claimed []store.Claimed) ([]store.Claimed, error) {
	limit, perThread := p.Cfg.Batch.TopUpItems, p.Cfg.Grouping.MaxItems
	if limit <= 0 || perThread <= 1 || route == core.RoutePost {
		return claimed, nil
	}
	have := map[string]int{}
	var order []string
	for _, c := range claimed {
		t := c.Item.ThreadID
		if t == "" || c.Item.Kind == core.KindPost || soloRetry(c) {
			continue
		}
		if have[t] == 0 {
			order = append(order, t)
		}
		have[t]++
	}
	var threads []string
	var room []int
	for _, t := range order {
		if n := perThread - have[t]; n > 0 {
			threads, room = append(threads, t), append(room, n)
		}
	}
	more, err := p.Store.ClaimThreads(ctx, core.LaneBatch, route, threads, room, limit, p.submitLease())
	if err != nil {
		return claimed, err
	}
	if len(more) > 0 {
		ametrics.ToppedUp.WithLabelValues(route).Add(float64(len(more)))
		slog.Info("analysis: batch round topped up its threads", "route", route, "claimed", len(claimed), "added", len(more),
			"threads", len(threads))
	}
	return append(claimed, more...), nil
}

// claimedItems are the claimed entries' items.
func claimedItems(cs []store.Claimed) []core.Item {
	out := make([]core.Item, len(cs))
	for i, c := range cs {
		out[i] = c.Item
	}
	return out
}

// submitLease is how long claimed batch items stay leased until their
// batch is sent (batch.submit_lease); SetBatchID then leases them for the
// batch's lifetime (batch.lease).
func (p *Pipeline) submitLease() time.Duration {
	if d := p.Cfg.Batch.SubmitLease; d > 0 {
		return d
	}
	return 15 * time.Minute
}

func (p *Pipeline) submitRoute(ctx context.Context, route, prov, model string) error {
	batcher, hasBatch := p.Providers.Batcher(prov)
	// A route whose day (or the day's total) is already spent claims
	// nothing: building and releasing a round's requests would cost the
	// database for no request.
	if spent, err := p.Budget.Exhausted(ctx, route); err != nil {
		return err
	} else if spent {
		slog.Info("analysis: batch round skipped: the budget is spent for today", "route", route)
		return nil
	}
	claimed, err := p.Store.Claim(ctx, core.LaneBatch, route, p.Cfg.Batch.MaxItems, p.submitLease(), 0)
	if err != nil || len(claimed) == 0 {
		return err
	}
	if claimed, err = p.topUp(ctx, route, claimed); err != nil {
		// What was claimed goes back now rather than waiting for its lease.
		return errors.Join(err, p.Store.ReleaseUnsent(context.WithoutCancel(ctx), ids(claimedItems(claimed)), route, 0))
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
	// reqs are the requests built and started but not yet sent. Each batch
	// goes out as soon as batch.max_requests of them are built, so a
	// round's first batch leaves within a minute or two, not after the
	// whole round is built, and a stopped round loses at most one batch's
	// work.
	var reqs []pending
	size := max(p.Cfg.Batch.MaxRequests, 1)
	// handled are the claimed items already started as a request (sent or
	// not) or released (over budget).
	handled := map[string]bool{}
	// releaseRest gives back the claimed items not yet started as a
	// request, so none waits for its lease to run out.
	releaseRest := func() error {
		var rest []string
		for _, c := range claimed {
			if !handled[c.Item.ContentID] {
				rest = append(rest, c.Item.ContentID)
			}
		}
		if len(rest) == 0 {
			return nil
		}
		return p.Store.ReleaseUnsent(context.WithoutCancel(ctx), rest, route, 5*time.Minute)
	}
	// giveBack, on an error before the requests built were sent, closes
	// them and releases the rest, so no estimate keeps counting against
	// the budget. Batches already sent are followed as usual.
	giveBack := func(cause error) error {
		errs := []error{cause}
		if len(reqs) > 0 {
			started := make([]uuid.UUID, len(reqs))
			for i, r := range reqs {
				started[i] = r.row.ID
			}
			errs = append(errs, p.Store.AbortRequests(context.WithoutCancel(ctx), started, "batch round failed: "+cause.Error(), 5*time.Minute))
		}
		errs = append(errs, releaseRest())
		return errors.Join(errs...)
	}
	// send submits the requests built so far as one batch. done reports
	// that the round is over: the batch couldn't be sent (its requests are
	// closed; with the fallback in use, they and the groups not yet built,
	// later, go to it in real time) or can't be followed.
	send := func(later []group) (done bool, err error) {
		if len(reqs) == 0 {
			return false, nil
		}
		chunk := reqs
		reqs = nil
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
			if aerr := p.Store.AbortRequests(context.WithoutCancel(ctx), reqIDs, "batch submit: "+err.Error(), 5*time.Minute); aerr != nil {
				return true, errors.Join(err, aerr)
			}
			if !useFallback {
				return true, errors.Join(err, releaseRest())
			}
			// Down for longer than batch.fallback_after: this batch and the
			// groups not yet built go to the fallback in real time (the
			// aborted items stay unclaimable for 5 minutes meanwhile).
			slog.Warn("analysis: batch provider down; using the fallback", "route", route, "provider", prov, "since", since, "err", err)
			var errs []error
			for _, r := range chunk {
				if rerr := p.RunGroup(ctx, r.g, core.LaneBatch); rerr != nil {
					errs = append(errs, rerr)
				}
			}
			for _, g := range later {
				if rerr := p.RunGroup(ctx, g, core.LaneBatch); rerr != nil {
					errs = append(errs, rerr)
				}
			}
			return true, errors.Join(errs...)
		}
		p.markUp(prov)
		if err := p.Store.SetBatchID(ctx, id, prov, model, route, reqIDs, p.Cfg.Batch.Lease); err != nil {
			// This batch was sent but can't be followed: ReleaseStrandedBatch
			// closes its requests after batch.submit_lease, and its items are
			// sent again. The groups not yet built are released now.
			return true, errors.Join(err, releaseRest())
		}
		ametrics.Requests.WithLabelValues(prov, model, core.LaneBatch, "submitted").Add(float64(len(chunk)))
		slog.Info("analysis: batch submitted", "route", route, "model", model, "batch", id, "requests", len(chunk))
		return false, nil
	}
	for gi, g := range groups {
		b, err := p.build(ctx, g, model)
		if err != nil {
			return giveBack(err)
		}
		id := uuid.New()
		b.req.ID = id.String()
		est := p.estimate(b, true)
		ok, err := p.Budget.Allow(ctx, route, est, false)
		if err != nil {
			return giveBack(err)
		}
		if !ok {
			// Over the route's or the day's cap: this group and the ones
			// not built yet wait, in one release rather than a build and a
			// release per group.
			var rest []string
			for _, r := range groups[gi:] {
				rest = append(rest, ids(r.items)...)
			}
			if err := p.Store.ReleaseUnsent(ctx, rest, route, 10*time.Minute); err != nil {
				return giveBack(err)
			}
			for _, cid := range rest {
				handled[cid] = true
			}
			ametrics.BudgetHeld.WithLabelValues(route).Add(float64(len(rest)))
			slog.Info("analysis: batch round held by the budget", "route", route, "requests_built", len(reqs), "items_held", len(rest))
			break
		}
		p.Budget.Add(route, est)
		row := store.RequestRow{ID: id, Route: route, Lane: core.LaneBatch, Provider: prov, Model: model,
			ContentIDs: ids(b.items), InputHashes: b.hashes, EstCostUSD: est}
		if err := p.Store.StartRequest(ctx, row); err != nil {
			return giveBack(err)
		}
		for _, cid := range row.ContentIDs {
			handled[cid] = true
		}
		reqs = append(reqs, pending{row: row, req: b.req, g: g})
		if len(reqs) >= size {
			if done, err := send(groups[gi+1:]); done || err != nil {
				return err
			}
		}
	}
	_, err = send(nil)
	return err
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
