package ingest

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Gate holds new content back while the analysis queue is full: Full turns
// true when the queue reaches Max and false again only once it has fallen
// to Resume, so ingest does not flap at the edge. Kafka keeps what waits.
type Gate struct {
	Max, Resume int64
	// Every is how often the queue is counted.
	Every time.Duration
	// Depth counts the queue.
	Depth func(context.Context) (int64, error)

	full    bool
	checked time.Time
	now     func() time.Time
}

// Full reports whether ingest of new content should wait. The queue is
// counted at most every Every; a failed count keeps the last answer.
func (g *Gate) Full(ctx context.Context) bool {
	now := time.Now
	if g.now != nil {
		now = g.now
	}
	if !g.checked.IsZero() && now().Sub(g.checked) < g.Every {
		return g.full
	}
	g.checked = now()
	n, err := g.Depth(ctx)
	if err != nil {
		slog.Warn("ingest: queue depth unknown; keeping the last state", "full", g.full, "err", err)
		return g.full
	}
	switch {
	case !g.full && n >= g.Max:
		g.full = true
	case g.full && n <= g.Resume:
		g.full = false
	}
	return g.full
}

// SharedDepth lets gates that are checked together share one count: a
// reading is reused for window after it was taken. The comment and post
// gates, checked one after the other, then count the queue once, not twice.
func SharedDepth(depth func(context.Context) (int64, error), window time.Duration) func(context.Context) (int64, error) {
	return sharedDepth(depth, window, time.Now)
}

func sharedDepth(depth func(context.Context) (int64, error), window time.Duration, now func() time.Time) func(context.Context) (int64, error) {
	var (
		mu sync.Mutex
		at time.Time
		n  int64
	)
	return func(ctx context.Context) (int64, error) {
		mu.Lock()
		defer mu.Unlock()
		if !at.IsZero() && now().Sub(at) < window {
			return n, nil
		}
		start := now()
		v, err := depth(ctx)
		if err != nil {
			return 0, err
		}
		n, at = v, start
		return n, nil
	}
}
