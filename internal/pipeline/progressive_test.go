package pipeline_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider/fake"
)

// watchingBatcher records, at each batch sent, how many requests the round
// had built by then, and fails the batch numbered failAt (from 1; 0 never).
type watchingBatcher struct {
	*fake.Provider
	built  func() int
	failAt int

	mu   sync.Mutex
	seen []int
}

func (w *watchingBatcher) SubmitBatch(ctx context.Context, reqs []provider.Request) (string, error) {
	w.mu.Lock()
	w.seen = append(w.seen, w.built())
	n := len(w.seen)
	w.mu.Unlock()
	if n == w.failAt {
		return "", errors.New("fake: batch refused")
	}
	return w.Provider.SubmitBatch(ctx, reqs)
}

// oneItemBatches makes each comment a request of its own, and each request
// a batch of its own.
func oneItemBatches(c *config.Analysis) { c.Grouping.MaxItems, c.Batch.MaxRequests = 1, 1 }

// TestBatchSentAsSoonAsBuilt: a round sends each batch as soon as its
// requests are built (batch.max_requests), not after building the whole
// round, so its first batch leaves at once.
func TestBatchSentAsSoonAsBuilt(t *testing.T) {
	e := setup(t, oneItemBatches)
	ctx := context.Background()
	w := &watchingBatcher{Provider: e.primary}
	w.built = func() int { return e.count(t, `SELECT count(*) FROM analysis.provider_requests`) }
	e.p.Providers.Add("primary", w, true, 0)
	e.ingest(t, comment(1, "الخدمة ممتازة"), comment(2, "الشبكة ضعيفة اليوم"), comment(3, "متى يرجع العرض"))
	if err := e.p.SubmitBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if len(w.seen) != 3 || w.seen[0] != 1 || w.seen[1] != 2 || w.seen[2] != 3 {
		t.Fatalf("requests built when each batch was sent: %v, want [1 2 3]", w.seen)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.provider_batches WHERE status = 'in_progress'`); n != 3 {
		t.Fatalf("%d batches in progress, want 3", n)
	}
}

// TestBatchRefusedMidRound: when a batch can't be sent, the batches sent
// before it are followed as usual, its requests are closed, and the items
// not yet built are released at once; nothing waits for a lease.
func TestBatchRefusedMidRound(t *testing.T) {
	e := setup(t, oneItemBatches)
	ctx := context.Background()
	w := &watchingBatcher{Provider: e.primary, failAt: 2}
	w.built = func() int { return 0 }
	e.p.Providers.Add("primary", w, true, 0)
	e.ingest(t, comment(1, "الخدمة ممتازة"), comment(2, "الشبكة ضعيفة اليوم"), comment(3, "متى يرجع العرض"))
	if err := e.p.SubmitBatches(ctx); err == nil {
		t.Fatal("a refused batch reported no error")
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.provider_batches WHERE status = 'in_progress'`); n != 1 {
		t.Fatalf("%d batches in progress, want the first one", n)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.provider_requests WHERE status = 'failed'`); n != 1 {
		t.Fatalf("%d requests closed, want the refused batch's one", n)
	}
	// The first batch's item is leased for the batch's lifetime; the
	// refused one and the one never built are free again, due later.
	if n := e.count(t, `SELECT count(*) FROM analysis.analysis_queue WHERE lease_until > now() + interval '1 hour'`); n != 1 {
		t.Fatalf("%d items leased for a batch, want 1", n)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analysis_queue WHERE lease_until IS NULL AND request_id IS NULL AND not_before > now()`); n != 2 {
		t.Fatalf("%d items given back, want 2 (refused and never built)", n)
	}
}

// TestBatchFirstRoundAtStart: the batch loop sends its first round as it
// starts, not one batch.submit_every later.
func TestBatchFirstRoundAtStart(t *testing.T) {
	e := setup(t, func(c *config.Analysis) { c.Batch.SubmitEvery, c.Batch.PollEvery = time.Hour, time.Hour })
	e.ingest(t, comment(1, "الخدمة ممتازة"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.p.RunBatch(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for e.count(t, `SELECT count(*) FROM analysis.provider_batches`) == 0 {
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("no batch sent at start (the first round waited for batch.submit_every)")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
