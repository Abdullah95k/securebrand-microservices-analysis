package provider

import (
	"errors"
	"math"
	"testing"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
)

func TestCost(t *testing.T) {
	p := Pricing{"claude-haiku-5-5": {Input: 0.10, Output: 0.50, CacheRead: 0.01, BatchDiscount: 0.5}}
	u := Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000, CacheReadTokens: 1_000_000, CacheWriteTokens: 1_000_000}
	if got := p.Cost("claude-haiku-5-5", u, false); math.Abs(got-(0.10+0.50+0.01+0.125)) > 1e-9 {
		t.Fatalf("realtime cost %v", got)
	}
	if got := p.Cost("claude-haiku-5-5", u, true); math.Abs(got-(0.10+0.50+0.01+0.125)/2) > 1e-9 {
		t.Fatalf("batch cost %v", got)
	}
	if got := p.Cost("anthropic.claude-haiku-5-5", Usage{OutputTokens: 2_000_000}, false); got != 1.0 {
		t.Fatalf("prefixed model cost %v", got)
	}
	if p.Cost("unknown", u, false) != 0 || p.Known("unknown") || !p.Known("claude-haiku-5-5") || !p.Known("fake") {
		t.Fatal("unknown model")
	}
	// A one-comment batch request on Haiku 5.5 as production sent them
	// on 11 Nov: a 23,237-byte prefix read from the cache, about 205 input
	// and 148 output tokens, $0.000113 measured. Expected within 10%; the
	// worst case, the prefix written to the cache, about 7.7 times that;
	// half and half, halfway.
	haiku := Pricing{"claude-haiku-5-5": {Input: 0.10, Output: 0.50, CacheRead: 0.01, CacheWrite: 0.125, BatchDiscount: 0.5}}
	est := haiku.Estimate("claude-haiku-5-5", 23237, 410, 0, 148, true)
	if math.Abs(est.HitUSD-0.000113) > 0.0000113 || est.At(1) != est.HitUSD {
		t.Fatalf("batch estimate $%v, want $0.000113 within 10%%", est.HitUSD)
	}
	if est.MissUSD < 0.00085 || est.MissUSD > 0.0009 || est.At(0) != est.MissUSD || est.At(-1) != est.MissUSD || est.At(2) != est.HitUSD {
		t.Fatalf("batch worst case $%v, want about $0.000875", est.MissUSD)
	}
	if math.Abs(est.At(0.5)-(est.HitUSD+est.MissUSD)/2) > 1e-15 {
		t.Fatalf("half the prefix cached: $%v", est.At(0.5))
	}
	// Real time: the same at full price.
	if rt := haiku.Estimate("claude-haiku-5-5", 23237, 410, 0, 148, false); math.Abs(rt.HitUSD-2*est.HitUSD) > 1e-12 || math.Abs(rt.MissUSD-2*est.MissUSD) > 1e-12 {
		t.Fatalf("real-time estimate %+v, want twice the batch's %+v", rt, est)
	}
	if (Pricing(map[string]config.ModelPrice{}).Estimate("x", 1, 1, 1, 1, false) != Est{}) {
		t.Fatal("estimate without prices")
	}
}

func TestRetryable(t *testing.T) {
	for status, want := range map[int]bool{400: false, 401: false, 404: false, 408: true, 409: true, 429: true, 500: true, 529: true} {
		if StatusRetryable(status) != want {
			t.Errorf("status %d", status)
		}
	}
	if !Retryable(errors.New("network")) || Retryable(nil) || Retryable(&Error{Status: 400}) || !Retryable(&Error{Retryable: true}) {
		t.Fatal("Retryable")
	}
	e := &Error{Status: 429, Err: errors.New("slow down")}
	if e.Error() == "" || !errors.Is(e, e.Err) {
		t.Fatal("error formatting / unwrap")
	}
	if (Usage{1, 2, 3, 4}).Add(Usage{1, 1, 1, 1}) != (Usage{2, 3, 4, 5}) {
		t.Fatal("usage add")
	}
}
