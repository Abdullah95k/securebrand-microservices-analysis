package run

import (
	"context"
	"sync"
	"time"
)

// Idle paces workers that poll one shared queue in the database. While
// the queue is empty, only one of them polls, and the wait between its
// polls doubles from Min up to Max; the others wait until work turns up.
// Busy (after a worker found work) wakes them all and sets the wait back to
// Min. So an idle process sends one query per wait instead of one per
// worker per Min, and PgBouncer can let its server connections go.
//
// Max at or below Min means a fixed wait of Min, the old behaviour.
type Idle struct {
	Min, Max time.Duration

	mu     sync.Mutex
	delay  time.Duration
	poller bool
	wake   chan struct{}
}

// Busy reports that a worker found work: every waiting worker wakes, and
// the next empty poll waits Min again.
func (i *Idle) Busy() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.delay = 0
	if i.wake != nil {
		close(i.wake)
		i.wake = nil
	}
}

// Wait is called by a worker that found nothing. If no other worker is
// polling, it becomes the poller: it sleeps the current wait (which then
// doubles, up to Max) and returns to poll. Otherwise it waits for Busy.
// Either way it returns early when ctx ends.
func (i *Idle) Wait(ctx context.Context) {
	i.mu.Lock()
	if i.wake == nil {
		i.wake = make(chan struct{})
	}
	wake := i.wake
	if i.poller {
		i.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-wake:
		}
		return
	}
	i.poller = true
	d := i.next()
	i.mu.Unlock()

	t := time.NewTimer(d)
	select {
	case <-ctx.Done():
	case <-wake:
	case <-t.C:
	}
	t.Stop()
	i.mu.Lock()
	i.poller = false
	i.mu.Unlock()
}

// next returns the wait before the poller's next poll and doubles the one
// after it, up to Max. Called with mu held.
func (i *Idle) next() time.Duration {
	upper := max(i.Max, i.Min)
	d := i.delay
	if d < i.Min {
		d = i.Min
	}
	i.delay = min(2*d, upper)
	return d
}
