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
		for _, g := range p.Groups(ctx, route, claimed) {
			if err := p.RunGroup(ctx, g, core.LaneRealtime); err != nil {
				errs = append(errs, err)
			}
			total += len(g.items)
		}
	}
	return total, errors.Join(errs...)
}

// RunBatch submits batch-lane items at start and then every
// batch.submit_every, and collects finished batches at start and then every
// batch.poll_every, in two loops of their own, until ctx ends. Collecting a
// batch can take minutes (its results are written a request at a time):
// with both in one loop, a poll collecting several batches held back every
// submission, and on 11 Nov no batch was sent for over an hour.
func (p *Pipeline) RunBatch(ctx context.Context) error {
	var wg sync.WaitGroup
	loop := func(name string, every time.Duration, fn func(context.Context) error) {
		defer wg.Done()
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			// The first run is at start, not one interval later: a restart
			// then delays the queue by a round's build time only.
			if err := fn(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("analysis: "+name, "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}
	wg.Add(2)
	go loop("batch submit", p.Cfg.Batch.SubmitEvery, p.SubmitBatches)
	go loop("batch poll", p.Cfg.Batch.PollEvery, p.PollBatches)
	wg.Wait()
	return nil
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
// comments, and each claimed page's other waiting short posts (posts on
// the comment route, grouped by page), up to grouping.max_items a thread
// or page and batch.top_up_items in all, threads first: a claim of the
// oldest items holds one or two items of most threads and pages, and
// Groups packs only what was claimed. Items retried after a fault of their
// own answer (sent alone) don't take part, nor does the post route (long
// posts pack together whatever their page).
func (p *Pipeline) topUp(ctx context.Context, route string, claimed []store.Claimed) ([]store.Claimed, error) {
	limit, per := p.Cfg.Batch.TopUpItems, p.Cfg.Grouping.MaxItems
	if limit <= 0 || per <= 1 || route == core.RoutePost {
		return claimed, nil
	}
	var threads, pages keyRoom
	for _, c := range claimed {
		switch {
		case soloRetry(c):
		case c.Item.Kind == core.KindPost:
			pages.add(c.Item.SourceID)
		default:
			threads.add(c.Item.ThreadID)
		}
	}
	tk, tr := threads.room(per)
	more, err := p.Store.ClaimThreads(ctx, core.LaneBatch, route, tk, tr, limit, p.submitLease())
	if err != nil {
		return claimed, err
	}
	pk, pr := pages.room(per)
	posts, err := p.Store.ClaimPages(ctx, core.LaneBatch, route, pk, pr, limit-len(more), p.submitLease())
	claimed = append(append(claimed, more...), posts...)
	if err != nil {
		return claimed, err
	}
	if n := len(more) + len(posts); n > 0 {
		ametrics.ToppedUp.WithLabelValues(route).Add(float64(n))
		slog.Info("analysis: batch round topped up its threads and pages", "route", route, "claimed", len(claimed)-n,
			"added", n, "threads", len(tk), "comments_added", len(more), "pages", len(pk), "posts_added", len(posts))
	}
	return claimed, nil
}

// keyRoom counts claimed items by thread or page, in the order first seen.
type keyRoom struct {
	order []string
	have  map[string]int
}

func (k *keyRoom) add(key string) {
	if key == "" {
		return
	}
	if k.have == nil {
		k.have = map[string]int{}
	}
	if k.have[key] == 0 {
		k.order = append(k.order, key)
	}
	k.have[key]++
}

// room is each key with room left under per, and how much.
func (k *keyRoom) room(per int) ([]string, []int) {
	var keys []string
	var room []int
	for _, key := range k.order {
		if n := per - k.have[key]; n > 0 {
			keys, room = append(keys, key), append(room, n)
		}
	}
	return keys, room
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
	// Shared across the platforms waiting, so a new collector's items
	// aren't held behind an older backlog (store.ClaimShared).
	claimed, err := p.Store.ClaimShared(ctx, core.LaneBatch, route, p.Cfg.Batch.MaxItems, p.submitLease())
	if err != nil {
		return err
	}
	if len(claimed) == 0 {
		slog.Info("analysis: batch round: nothing to claim", "route", route)
		return nil
	}
	// One line a round says what it did, so a round that sends nothing is
	// seen.
	start, first := p.now(), len(claimed)
	var sentRequests, batches int
	defer func() {
		slog.Info("analysis: batch round", "route", route, "claimed", first, "items", len(claimed),
			"requests_sent", sentRequests, "batches", batches, "seconds", int(p.now().Sub(start).Seconds()))
	}()
	if claimed, err = p.topUp(ctx, route, claimed); err != nil {
		// What was claimed goes back now rather than waiting for its lease.
		return errors.Join(err, p.Store.ReleaseUnsent(context.WithoutCancel(ctx), ids(claimedItems(claimed)), route, 0))
	}
	groups := p.Groups(ctx, route, claimed)
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
		slog.Warn("analysis: batch provider unavailable (circuit open); the round's items wait 5 minutes",
			"route", route, "provider", prov, "items", len(claimed))
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
		sentRequests, batches = sentRequests+len(chunk), batches+1
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
		est, worst := p.estimate(ctx, route, b, true)
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
			ContentIDs: ids(b.items), InputHashes: b.hashes, EstCostUSD: est, EstMaxUSD: worst}
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

// pollMax is the most open batches one poll checks.
const pollMax = 10

// collectLease is how long a batch stays claimed for its collection: a
// thousand results took about 6 minutes in production on 11 Nov.
const collectLease = 30 * time.Minute

// PollBatches checks up to pollMax open batches, oldest first, and collects
// the finished ones. Each is claimed as it is checked, so the lease covers
// its own collection; the ones not finished are released when the poll
// ends, for the next poll.
func (p *Pipeline) PollBatches(ctx context.Context) error {
	if n, err := p.Store.OpenBatchCount(ctx); err == nil {
		ametrics.OpenBatches.Set(float64(n))
	}
	var errs []error
	var waiting []string
	for range pollMax {
		open, err := p.Store.ClaimOpenBatches(ctx, 1, collectLease)
		if err != nil {
			errs = append(errs, err)
			break
		}
		if len(open) == 0 {
			break
		}
		b := open[0]
		done, err := p.collect(ctx, b)
		switch {
		case err != nil:
			errs = append(errs, err)
			_ = p.Store.ReleaseBatch(context.WithoutCancel(ctx), b.ID)
		case !done:
			waiting = append(waiting, b.ID)
		}
	}
	for _, id := range waiting {
		if err := p.Store.ReleaseBatch(context.WithoutCancel(ctx), id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// collect collects a batch if it has finished, and reports whether it had.
func (p *Pipeline) collect(ctx context.Context, b store.OpenBatch) (bool, error) {
	batcher, ok := p.Providers.Batcher(b.Provider)
	if !ok {
		return true, p.Store.EndBatch(ctx, b.ID, "failed", "provider "+b.Provider+" has no batch API configured")
	}
	done, err := batcher.BatchDone(ctx, b.ID)
	if err != nil || !done {
		return false, err
	}
	start := p.now()
	results, err := batcher.BatchResults(ctx, b.ID)
	if err != nil {
		return true, err
	}
	for _, r := range results {
		if err := p.collectOne(ctx, r); err != nil {
			return true, err
		}
	}
	stale, err := p.Store.StaleRequests(ctx, b.ID)
	if err != nil {
		return true, err
	}
	for _, id := range stale {
		if err := p.Store.FailRequest(ctx, id, "no result in batch", p.Cfg.Limits.MaxAttempts); err != nil {
			return true, err
		}
	}
	slog.Info("analysis: batch collected", "batch", b.ID, "route", b.Route, "results", len(results),
		"seconds", int(p.now().Sub(start).Seconds()))
	return true, p.Store.EndBatch(ctx, b.ID, "ended", "")
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
