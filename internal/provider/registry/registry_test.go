package registry

import (
	"testing"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
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
