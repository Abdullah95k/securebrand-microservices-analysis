package core

import (
	"fmt"
	"math"
	"testing"
)

// The sample is stable (the same ID always gets the same answer), close to
// its share, and nested: what a smaller share takes, a larger one takes too.
func TestInSample(t *testing.T) {
	const n = 200000
	in10, in25 := 0, 0
	for i := range n {
		id := fmt.Sprintf("tt:comment:%d", i)
		a, b := InSample(id, 0.10), InSample(id, 0.25)
		if a != InSample(id, 0.10) {
			t.Fatalf("%s: not stable", id)
		}
		if a && !b {
			t.Fatalf("%s: in the 10%% sample but not the 25%% one", id)
		}
		if a {
			in10++
		}
		if b {
			in25++
		}
	}
	for _, c := range []struct {
		got   int
		share float64
	}{{in10, 0.10}, {in25, 0.25}} {
		// Within 5 standard deviations of n*share.
		if sd := math.Sqrt(n * c.share * (1 - c.share)); math.Abs(float64(c.got)-n*c.share) > 5*sd {
			t.Errorf("share %v took %d of %d", c.share, c.got, n)
		}
	}
	if !InSample("x", 1) || !InSample("x", 2) || InSample("x", 0) || InSample("x", -1) {
		t.Error("shares of 1 and above take everything, 0 and below nothing")
	}
}
