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
	// A comment's estimate on Haiku batch is a few micro-dollars.
	est := p.Estimate("claude-haiku-5-5", 9000, 600, 0, 80, true)
	if est <= 0 || est > 0.0001 {
		t.Fatalf("estimate %v", est)
	}
	if Pricing(map[string]config.ModelPrice{}).Estimate("x", 1, 1, 1, 1, false) != 0 {
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
