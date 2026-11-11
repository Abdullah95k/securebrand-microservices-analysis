package ingest

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGateHysteresisAndPace(t *testing.T) {
	depth, counts := int64(0), 0
	var fail error
	clock := time.Unix(0, 0)
	g := &Gate{Max: 10, Resume: 5, Every: time.Second, now: func() time.Time { return clock },
		Depth: func(context.Context) (int64, error) { counts++; return depth, fail }}
	ctx := context.Background()
	step := func(d int64, advance time.Duration, want bool) {
		t.Helper()
		depth, clock = d, clock.Add(advance)
		if got := g.Full(ctx); got != want {
			t.Fatalf("depth %d: full %v, want %v", d, got, want)
		}
	}
	step(9, time.Second, false)
	step(10, time.Second, true)       // reaches the cap
	step(7, time.Second, true)        // below the cap but above resume: still full
	step(5, time.Second, false)       // down to resume
	step(9, time.Second, false)       // below the cap again
	step(12, time.Millisecond, false) // not recounted within Every
	if counts != 5 {
		t.Fatalf("counted %d times, want 5", counts)
	}
	step(12, time.Second, true)
	fail = errors.New("database down")
	step(0, time.Second, true) // an unknown depth keeps the last answer
}

// TestGatesShareOneCount: the comment and post gates, checked one after the
// other, count the queue once per check; a failed count isn't reused.
func TestGatesShareOneCount(t *testing.T) {
	counts := 0
	var fail error
	clock := time.Unix(0, 0)
	now := func() time.Time { return clock }
	depth := sharedDepth(func(context.Context) (int64, error) { counts++; return 12, fail }, 500*time.Millisecond, now)
	comments := &Gate{Max: 10, Resume: 5, Every: time.Second, now: now, Depth: depth}
	posts := &Gate{Max: 50, Resume: 40, Every: time.Second, now: now, Depth: depth}
	ctx := context.Background()
	for i := range 3 {
		clock = clock.Add(time.Second)
		if !comments.Full(ctx) || posts.Full(ctx) {
			t.Fatal("want comments full at 12, posts not")
		}
		if counts != i+1 {
			t.Fatalf("check %d: counted %d times, want %d", i+1, counts, i+1)
		}
	}
	fail = errors.New("database down")
	clock = clock.Add(time.Second)
	comments.Full(ctx)
	posts.Full(ctx)
	if counts != 5 {
		t.Fatalf("after a failed count: counted %d times, want 5 (the failure not reused)", counts)
	}
}
