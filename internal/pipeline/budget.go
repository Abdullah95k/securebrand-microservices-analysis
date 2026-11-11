package pipeline

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/ametrics"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
)

// Budget caps spend per day (in the store's timezone), globally and per
// route. Spend comes from provider_requests (actual cost, or the expected
// cost of requests still in flight: store.SpendByRoute), refreshed every
// 30 seconds, plus what this process added since. It also keeps each
// route's measure (output per item, prefix cache hits), which the
// estimate prices requests at.
type Budget struct {
	Store        *store.Store
	DailyUSD     float64
	RouteUSD     map[string]float64
	UrgentExempt bool

	mu        sync.Mutex
	day       time.Time
	refreshed time.Time
	spent     float64
	route     map[string]float64
	now       func() time.Time

	measuredAt time.Time
	measures   map[string]store.RouteMeasure
}

// DefaultOutPerItem is the output tokens a request is estimated at per
// item while its route has no measure (fewer than store.MinMeasuredRequests
// answered in the last day): one-comment requests answered with 148 on
// 11 Nov. Without a measure the prefix is estimated as a cache miss.
const DefaultOutPerItem = 150

// measureEvery is how often the routes' measures are read again; they
// are over the last day, so they move slowly.
const measureEvery = 10 * time.Minute

// Measure is what the estimate prices the route's requests at: its
// output tokens per item and the share of its prefix read from the cache,
// over the last day's answered requests (store.RouteMeasures, read every
// 10 minutes). Without a measure: DefaultOutPerItem, and the prefix
// missing the cache. If the measures can't be read, the last ones stand.
func (b *Budget) Measure(ctx context.Context, route string) store.RouteMeasure {
	def := store.RouteMeasure{OutPerItem: DefaultOutPerItem}
	if b == nil || b.Store == nil {
		return def
	}
	now := b.clock()
	b.mu.Lock()
	stale := b.measuredAt.IsZero() || now.Sub(b.measuredAt) >= measureEvery
	if stale {
		b.measuredAt = now // one reader at a time
	}
	b.mu.Unlock()
	if stale {
		m, err := b.Store.RouteMeasures(ctx, now.Add(-24*time.Hour))
		if err != nil {
			slog.Warn("analysis: route measures not read, the last ones stand", "err", err)
		} else {
			b.mu.Lock()
			b.measures = m
			b.mu.Unlock()
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if m, ok := b.measures[route]; ok && m.OutPerItem > 0 {
		return m
	}
	return def
}

func (b *Budget) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// limited reports whether any cap is configured.
func (b *Budget) limited() bool {
	if b.DailyUSD > 0 {
		return true
	}
	for _, v := range b.RouteUSD {
		if v > 0 {
			return true
		}
	}
	return false
}

// Allow reports whether a request estimated at est USD may run now.
func (b *Budget) Allow(ctx context.Context, route string, est float64, urgent bool) (bool, error) {
	if b == nil || !b.limited() {
		return true, nil
	}
	if urgent && b.UrgentExempt {
		return true, nil
	}
	if err := b.refresh(ctx); err != nil {
		return false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	ok := b.DailyUSD <= 0 || b.spent+est <= b.DailyUSD
	if limit := b.RouteUSD[route]; limit > 0 && b.route[route]+est > limit {
		ok = false
	}
	if ok {
		ametrics.BudgetPaused.Set(0)
	} else {
		ametrics.BudgetPaused.Set(1)
	}
	return ok, nil
}

// Exhausted reports whether the day's cap, or the route's, is already
// reached: no request of that route can run until the next day. A caller
// with expensive preparation (the media worker fetching and decoding a
// file) asks this first, so a paused budget costs no work.
func (b *Budget) Exhausted(ctx context.Context, route string) (bool, error) {
	if b == nil || !b.limited() {
		return false, nil
	}
	if err := b.refresh(ctx); err != nil {
		return false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	done := b.DailyUSD > 0 && b.spent >= b.DailyUSD
	if limit := b.RouteUSD[route]; limit > 0 && b.route[route] >= limit {
		done = true
	}
	if done {
		ametrics.BudgetPaused.Set(1)
	}
	return done, nil
}

// Add records spend this process just committed to (until the next
// refresh reads it back from the database).
func (b *Budget) Add(route string, usd float64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.spent += usd
	if b.route == nil {
		b.route = map[string]float64{}
	}
	b.route[route] += usd
	ametrics.SpentToday.Set(b.spent)
	b.mu.Unlock()
}

func (b *Budget) refresh(ctx context.Context) error {
	now := b.clock()
	start := b.Store.DayStart(now)
	b.mu.Lock()
	fresh := start.Equal(b.day) && now.Sub(b.refreshed) < 30*time.Second
	b.mu.Unlock()
	if fresh {
		return nil
	}
	spend, err := b.Store.SpendByRoute(ctx, start)
	if err != nil {
		return err
	}
	var total float64
	routes := map[string]float64{}
	for _, r := range spend {
		total += r.Counted
		routes[r.Route] = r.Counted
	}
	b.mu.Lock()
	b.day, b.refreshed, b.spent, b.route = start, now, total, routes
	b.mu.Unlock()
	ametrics.SpentToday.Set(total)
	ametrics.BudgetUSD.Set(b.DailyUSD)
	return nil
}
