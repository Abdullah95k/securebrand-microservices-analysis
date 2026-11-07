package run

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestIdleWaitDoublesUpToMax: an idle poller waits Min, then twice as long
// each time, up to Max; Busy sets it back to Min.
func TestIdleWaitDoublesUpToMax(t *testing.T) {
	i := &Idle{Min: time.Second, Max: 10 * time.Second}
	var got []time.Duration
	for range 6 {
		got = append(got, i.next())
	}
	want := []time.Duration{1, 2, 4, 8, 10, 10}
	for n := range want {
		if got[n] != want[n]*time.Second {
			t.Fatalf("waits %v, want %v seconds", got, want)
		}
	}
	i.Busy()
	if d := i.next(); d != time.Second {
		t.Fatalf("after Busy the wait is %v, want 1s", d)
	}
	// Max at or below Min: a fixed wait, as before.
	fixed := &Idle{Min: 2 * time.Second}
	if a, b := fixed.next(), fixed.next(); a != 2*time.Second || b != 2*time.Second {
		t.Fatalf("no Max: waits %v, %v, want 2s each", a, b)
	}
}

// TestIdleOnePollerAtATime: while the queue is empty, four workers poll
// once per wait between them, not once each; Busy wakes them all at once.
func TestIdleOnePollerAtATime(t *testing.T) {
	i := &Idle{Min: 20 * time.Millisecond, Max: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var polls atomic.Int64
	var work atomic.Bool
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				polls.Add(1)
				if work.Load() {
					i.Busy()
					// One unit of work each, then the queue is empty again.
					time.Sleep(5 * time.Millisecond)
					continue
				}
				i.Wait(ctx)
			}
		}()
	}

	time.Sleep(300 * time.Millisecond)
	idle := polls.Load()
	// 300 ms at one poll per 20 ms: about 15, plus the four first polls.
	// Four workers polling each on their own would make about 60.
	if idle > 30 {
		t.Fatalf("%d polls in 300 ms while idle: more than one worker polls", idle)
	}

	// Work turns up: every worker wakes, without waiting out the poller.
	before := polls.Load()
	work.Store(true)
	time.Sleep(30 * time.Millisecond)
	if busy := polls.Load() - before; busy < 8 {
		t.Fatalf("%d polls in 30 ms with work: the waiting workers didn't wake", busy)
	}
	cancel()
	wg.Wait()
}

// TestIdleWaitEndsWithContext: neither the poller nor a waiting worker
// outlives its context.
func TestIdleWaitEndsWithContext(t *testing.T) {
	i := &Idle{Min: time.Hour, Max: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{}, 2)
	for range 2 {
		go func() { i.Wait(ctx); done <- struct{}{} }()
	}
	time.Sleep(10 * time.Millisecond)
	cancel()
	for range 2 {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Wait didn't return when its context ended")
		}
	}
}
