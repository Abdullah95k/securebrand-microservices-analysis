package pipeline

import (
	"context"
	"sync"
	"time"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/ametrics"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/store"
)

// Budget caps spend per day (in the store's timezone), globally and per
// route. Spend comes from provider_requests (actual cost, or the estimate
// of requests still in flight), refreshed every 30 seconds, plus what this
// process added since.
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
	ok := true
	if b.DailyUSD > 0 && b.spent+est > b.DailyUSD {
		ok = false
	}
	if cap := b.RouteUSD[route]; cap > 0 && b.route[route]+est > cap {
		ok = false
	}
	if ok {
		ametrics.BudgetPaused.Set(0)
	} else {
		ametrics.BudgetPaused.Set(1)
	}
	return ok, nil
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
	total, err := b.Store.SpentSince(ctx, start, "")
	if err != nil {
		return err
	}
	routes := map[string]float64{}
	for r, cap := range b.RouteUSD {
		if cap <= 0 {
			continue
		}
		v, err := b.Store.SpentSince(ctx, start, r)
		if err != nil {
			return err
		}
		routes[r] = v
	}
	b.mu.Lock()
	b.day, b.refreshed, b.spent, b.route = start, now, total, routes
	b.mu.Unlock()
	ametrics.SpentToday.Set(total)
	ametrics.BudgetUSD.Set(b.DailyUSD)
	return nil
}
