package ratelimit

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/time/rate"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/testenv"
)

// count runs every limiter flat out, from goroutines of its own, for d and
// returns how many requests were admitted in all.
func count(t *testing.T, d time.Duration, ls ...Limiter) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var n atomic.Int64
	var wg sync.WaitGroup
	for _, l := range ls {
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for l.Wait(ctx) == nil {
					n.Add(1)
				}
			}()
		}
	}
	wg.Wait()
	return n.Load()
}

// Four processes sharing one limit send, together, what one is allowed:
// limiters of their own each would send four times as much.
func TestSharedAcrossProcesses(t *testing.T) {
	rdb := testenv.Redis(t)
	key := fmt.Sprintf("test:rl:%d", rand.Int64())
	const rps, burst = 40, 2
	var shared []Limiter
	for range 4 {
		shared = append(shared, NewShared(rdb, key, rps, burst, 4))
	}
	d := 2 * time.Second
	got := count(t, d, shared...)
	max := int64(rps*d.Seconds()) + burst + 2
	if got > max || got < max/2 {
		t.Fatalf("4 processes sharing %d/s for %s sent %d requests, want about %d", rps, d, got, max-2)
	}
	var local []Limiter
	for range 4 {
		local = append(local, rate.NewLimiter(rps, burst))
	}
	if alone := count(t, d, local...); alone < 3*max {
		t.Fatalf("control: 4 limiters of their own sent %d, expected about 4 times %d", alone, max)
	}
	t.Logf("shared: %d requests in %s at %d/s (4 processes)", got, d, rps)
}

// With Valkey unreachable, a process falls back to its share of the rate
// (rps / fallback divisor) and reports it.
func TestFallbackWhenValkeyIsDown(t *testing.T) {
	down := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 50 * time.Millisecond, MaxRetries: -1})
	defer func() { _ = down.Close() }()
	l := NewShared(down, "test:rl:down", 40, 1, 4)
	var fellBack atomic.Int64
	l.OnFallback = func(error) { fellBack.Add(1) }
	d := 2 * time.Second
	got := count(t, d, l)
	// 40/4 = 10 a second, plus the first.
	if got < 10 || got > 23 {
		t.Fatalf("fell back to %d requests in %s, want about %d", got, d, 21)
	}
	if fellBack.Load() == 0 {
		t.Fatal("the fallback was not reported")
	}
}

// The admission time is stored as a plain integer of microseconds. (Valkey
// 8 writes such Lua numbers exactly anyway; the script formats them itself
// so an older server, which kept 14 digits, cannot round them.)
func TestTimesStayExact(t *testing.T) {
	rdb := testenv.Redis(t)
	key := fmt.Sprintf("test:rl:%d", rand.Int64())
	l := NewShared(rdb, key, 1000, 1, 1)
	for range 5 {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	v, err := rdb.Get(context.Background(), key).Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range v {
		if c < '0' || c > '9' {
			t.Fatalf("stored time %q is not a plain integer", v)
		}
	}
}
