package pipeline_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

// TestBatchSubmitsWhileCollecting: a batch's collection that takes long
// (held open here) doesn't hold back submission: a new item is sent in a
// batch of its own meanwhile. On 11 Nov both ran in one loop, and polls
// collecting several batches of 1,000 results held back every submission
// for over an hour.
func TestBatchSubmitsWhileCollecting(t *testing.T) {
	e := setup(t, func(c *config.Analysis) {
		c.Batch.SubmitEvery, c.Batch.PollEvery = 20*time.Millisecond, 10*time.Millisecond
	})
	collecting := make(chan struct{}, 1)
	e.primary.Collecting = func(ctx context.Context) error {
		select {
		case collecting <- struct{}{}:
		default:
		}
		<-ctx.Done() // held open until the loop stops
		return ctx.Err()
	}
	e.ingest(t, comment(1, "الخدمة ممتازة"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.p.RunBatch(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-collecting:
	case <-time.After(10 * time.Second):
		t.Fatal("the first batch was never sent and collected")
	}
	// The first batch's collection is held open; a new comment still goes
	// out.
	c := comment(2, "الشبكة ضعيفة")
	c.ThreadID, c.ParentID = "fb:post:2", "fb:post:2"
	e.ingest(t, c)
	deadline := time.Now().Add(10 * time.Second)
	for e.count(t, `SELECT count(*) FROM analysis.provider_batches`) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("no batch sent while a collection was held open: submission waited for it")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestPollChecksEachBatchOnce: a poll checks each open batch once,
// collects the finished ones, and releases the one still running for the
// next poll (not claimed again and again within the poll).
func TestPollChecksEachBatchOnce(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	for i := range 3 {
		c := comment(i+1, "تعليق")
		c.ThreadID, c.ParentID = fmt.Sprintf("fb:post:%d", i+10), fmt.Sprintf("fb:post:%d", i+10)
		e.ingest(t, c)
		if err := e.p.SubmitBatches(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.provider_batches WHERE status = 'in_progress'`); n != 3 {
		t.Fatalf("%d open batches, want 3", n)
	}
	var checks int
	var mu sync.Mutex
	e.primary.Unfinished = func(id string) bool {
		mu.Lock()
		defer mu.Unlock()
		if id == "fakebatch_2" {
			checks++
			return true
		}
		return false
	}
	if err := e.p.PollBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.provider_batches WHERE status = 'ended'`); n != 2 {
		t.Fatalf("%d batches collected, want the 2 finished", n)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.provider_batches WHERE status = 'in_progress' AND lease_until IS NULL`); n != 1 {
		t.Fatal("the running batch wasn't released for the next poll")
	}
	if checks != 1 {
		t.Fatalf("the running batch was checked %d times in one poll, want 1", checks)
	}
	// Once finished, the next poll collects it.
	e.primary.Unfinished = nil
	if err := e.p.PollBatches(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.provider_batches WHERE status = 'ended'`); n != 3 {
		t.Fatalf("%d batches collected after the next poll, want 3", n)
	}
}

// TestPollGoesOnPastAFailingBatch: a batch whose collection fails (here the
// provider doesn't know it) is held back, and the poll goes on to collect
// the others. On 12 Nov the failing batch, released at once and the
// oldest, was claimed again by every check of every poll, and no other
// finished batch was collected for hours.
func TestPollGoesOnPastAFailingBatch(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	for i := range 3 {
		c := comment(i+1, "تعليق")
		c.ThreadID, c.ParentID = fmt.Sprintf("fb:post:%d", i+20), fmt.Sprintf("fb:post:%d", i+20)
		e.ingest(t, c)
		if err := e.p.SubmitBatches(ctx); err != nil {
			t.Fatal(err)
		}
	}
	// The oldest batch now fails every check: the provider doesn't know it.
	if _, err := e.s.DB.Exec(ctx, `UPDATE analysis.provider_batches SET batch_id = 'fakebatch_gone' WHERE batch_id = 'fakebatch_1'`); err != nil {
		t.Fatal(err)
	}
	err := e.p.PollBatches(ctx)
	if err == nil || !strings.Contains(err.Error(), "fakebatch_gone") {
		t.Fatalf("poll error %v, want one naming the failing batch", err)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.provider_batches WHERE status = 'ended'`); n != 2 {
		t.Fatalf("%d batches collected past the failing one, want 2", n)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.provider_batches WHERE batch_id = 'fakebatch_gone' AND lease_until > now() + interval '5 minutes'`); n != 1 {
		t.Fatal("the failing batch wasn't held back")
	}
	// Held back: the next poll doesn't try it again yet.
	if err := e.p.PollBatches(ctx); err != nil {
		t.Fatalf("the held batch was tried again at once: %v", err)
	}
}
