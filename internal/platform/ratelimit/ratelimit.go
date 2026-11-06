// Package ratelimit spaces requests across every process of the service:
// a generic cell rate algorithm (GCRA) kept in Valkey, so N replicas
// together send at most the configured rate. Valkey's own clock decides, so
// the pods' clocks don't matter. When Valkey cannot be reached, each
// process falls back to a share of the rate on its own.
package ratelimit

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"
)

// Limiter waits until a request may be sent.
type Limiter interface {
	Wait(ctx context.Context) error
}

// gcra admits one request: it returns 0, or the microseconds to wait first.
// KEYS[1] holds the theoretical arrival time (TAT) in microseconds of
// Valkey's clock; ARGV[1] is the interval between requests, ARGV[2] the
// burst tolerance, both in microseconds. Times are written with %.0f, so no
// server version's number format can round a 16-digit time.
var gcra = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000000 + tonumber(t[2])
local interval = tonumber(ARGV[1])
local tolerance = tonumber(ARGV[2])
local tat = tonumber(redis.call('GET', KEYS[1]) or now)
if tat < now then tat = now end
local wait = tat - tolerance - now
if wait > 0 then return wait end
local new = tat + interval
redis.call('SET', KEYS[1], string.format('%.0f', new), 'PX', math.ceil((new - now + tolerance) / 1000) + 1000)
return 0
`)

// Shared is one provider's limit, shared through Valkey.
type Shared struct {
	client    redis.UniversalClient
	key       string
	interval  time.Duration
	tolerance time.Duration
	fallback  *rate.Limiter
	// OnFallback is called when Valkey fails and the local share is used.
	OnFallback func(err error)

	mu       sync.Mutex
	warnedAt time.Time
}

// NewShared limits key to rps requests per second (burst: as many at once)
// across every process using the same Valkey and key. fallbackDivisor
// splits the rate for the time Valkey is unreachable (the number of
// processes expected; at least 1).
func NewShared(client redis.UniversalClient, key string, rps float64, burst, fallbackDivisor int) *Shared {
	burst = max(burst, 1)
	interval := time.Duration(float64(time.Second) / rps)
	local := rps / float64(max(fallbackDivisor, 1))
	return &Shared{
		client: client, key: key, interval: interval, tolerance: time.Duration(burst-1) * interval,
		fallback: rate.NewLimiter(rate.Limit(local), max(1, burst/max(fallbackDivisor, 1))),
	}
}

// Wait blocks until the shared limit admits a request, or ctx ends.
func (s *Shared) Wait(ctx context.Context) error {
	for {
		wait, err := gcra.Run(ctx, s.client, []string{s.key}, s.interval.Microseconds(), s.tolerance.Microseconds()).Int64()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.fellBack(err)
			return s.fallback.Wait(ctx)
		}
		if wait <= 0 {
			return nil
		}
		t := time.NewTimer(time.Duration(wait) * time.Microsecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (s *Shared) fellBack(err error) {
	if s.OnFallback != nil {
		s.OnFallback(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.warnedAt) > time.Minute {
		s.warnedAt = time.Now()
		slog.Warn("rate limit: Valkey unreachable, limiting this process alone at its share", "key", s.key, "err", err)
	}
}
