package registry

import (
	"context"
	"testing"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
)

func TestKeylessHostedProvidersAreLeftOut(t *testing.T) {
	r, err := New(map[string]config.Provider{
		"anthropic":    {Kind: "anthropic"},
		"anthropic-ok": {Kind: "anthropic", APIKey: "k"},
		"gemini":       {Kind: "openai_compat", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai"},
		"whisper":      {Kind: "openai_compat", BaseURL: "http://whisper:8000/v1"},
		"fake":         {Kind: "fake"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"anthropic": false, "anthropic-ok": true, "gemini": false, "whisper": true, "fake": true} {
		if r.Has(name) != want {
			t.Errorf("Has(%s) = %v, want %v", name, r.Has(name), want)
		}
		// A provider left out is unavailable, so its route falls back.
		if r.Available(name) != want {
			t.Errorf("Available(%s) = %v, want %v", name, r.Available(name), want)
		}
	}
}

type countingLimiter struct{ waits int }

func (c *countingLimiter) Wait(context.Context) error { c.waits++; return nil }

// The service passes limits shared across processes (WithLimiters); every
// call to a provider with an rps then waits on that limiter.
func TestCallsWaitOnTheSharedLimiter(t *testing.T) {
	shared := map[string]*countingLimiter{}
	var rpsSeen float64
	r, err := New(map[string]config.Provider{"fake": {Kind: "fake", RPS: 7}, "free": {Kind: "fake"}},
		WithLimiters(func(name string, rps float64) Limiter {
			rpsSeen = rps
			shared[name] = &countingLimiter{}
			return shared[name]
		}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := shared["free"]; ok || rpsSeen != 7 {
		t.Fatalf("limiters built: %v, rps %v", shared, rpsSeen)
	}
	for range 3 {
		if _, err := r.Complete(context.Background(), "fake", provider.Request{ID: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if shared["fake"].waits != 3 {
		t.Fatalf("%d waits on the shared limiter, want 3", shared["fake"].waits)
	}
}
