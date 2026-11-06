package ingest

import (
	"context"
	"log/slog"
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
