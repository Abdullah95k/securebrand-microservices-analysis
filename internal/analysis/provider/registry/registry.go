// Package registry builds the configured providers and wraps every call
// with a rate limiter and a circuit breaker per provider.
package registry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"golang.org/x/time/rate"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/provider"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/provider/anthropic"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/provider/fake"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/provider/openaicompat"
	"github.com/abdullah95k/securebrand-microservices/internal/config"
	"github.com/abdullah95k/securebrand-microservices/internal/platform/breaker"
)

// Registry holds the providers by configured name.
type Registry struct {
	providers map[string]provider.Provider
	batch     map[string]bool
	limiters  map[string]*rate.Limiter
	breakers  *breaker.Set
}

// ErrUnavailable means the provider's circuit breaker is open.
var ErrUnavailable = &provider.Error{Retryable: true, Err: fmt.Errorf("provider unavailable (circuit open)")}

// New builds every configured provider.
func New(cfg map[string]config.Provider) (*Registry, error) {
	r := &Registry{providers: map[string]provider.Provider{}, batch: map[string]bool{}, limiters: map[string]*rate.Limiter{},
		breakers: breaker.New(breaker.Config{MinEvents: 10, OpenRate: 0.5})}
	names := make([]string, 0, len(cfg))
	for n := range cfg {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		c := cfg[name]
		// A hosted API without a key would fail every call: leave it out,
		// so a route's fallback is only used once its key is set. Self-
		// hosted servers (plain http, no key) are kept.
		if c.APIKey == "" && (c.Kind == "anthropic" || (c.Kind == "openai_compat" && strings.HasPrefix(c.BaseURL, "https://"))) {
			slog.Info("analysis: provider has no API key, not used", "provider", name)
			continue
		}
		var p provider.Provider
		switch c.Kind {
		case "anthropic":
			p = anthropic.New(anthropic.Config{Name: name, APIKey: c.APIKey, BaseURL: c.BaseURL, Timeout: c.Timeout, MaxRetries: c.MaxRetries})
		case "openai_compat":
			p = openaicompat.New(openaicompat.Config{Name: name, BaseURL: c.BaseURL, APIKey: c.APIKey, Timeout: c.Timeout,
				MaxRetries: c.MaxRetries, TranscribeModel: c.TranscribeModel})
		case "fake":
			p = fake.New(name)
		default:
			return nil, fmt.Errorf("provider %s: unknown kind %q", name, c.Kind)
		}
		r.Add(name, p, c.Batch || c.Kind == "fake", c.RPS)
	}
	return r, nil
}

// Add registers a provider (tests use it to install fakes).
func (r *Registry) Add(name string, p provider.Provider, useBatch bool, rps float64) {
	r.providers[name] = p
	r.batch[name] = useBatch
	if rps > 0 {
		r.limiters[name] = rate.NewLimiter(rate.Limit(rps), max(1, int(rps)))
	} else {
		delete(r.limiters, name)
	}
}

// Has reports whether a provider is configured (and has its key).
func (r *Registry) Has(name string) bool {
	_, ok := r.providers[name]
	return ok
}

// Get returns a provider.
func (r *Registry) Get(name string) (provider.Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// Batcher returns the provider's batch API when it has one and the config
// enables it.
func (r *Registry) Batcher(name string) (provider.Batcher, bool) {
	if !r.batch[name] {
		return nil, false
	}
	b, ok := r.providers[name].(provider.Batcher)
	return b, ok
}

// Transcriber returns the provider's speech-to-text, if any.
func (r *Registry) Transcriber(name string) (provider.Transcriber, bool) {
	t, ok := r.providers[name].(provider.Transcriber)
	return t, ok
}

// Available reports whether the provider's circuit is not open.
func (r *Registry) Available(name string) bool { return r.Has(name) && !r.breakers.IsOpen(name) }

// Complete calls a provider through its limiter and breaker.
func (r *Registry) Complete(ctx context.Context, name string, req provider.Request) (*provider.Response, error) {
	p, ok := r.providers[name]
	if !ok {
		return nil, fmt.Errorf("provider %s not configured", name)
	}
	if !r.breakers.Allow(name) {
		return nil, ErrUnavailable
	}
	if l := r.limiters[name]; l != nil {
		if err := l.Wait(ctx); err != nil {
			return nil, err
		}
	}
	resp, err := p.Complete(ctx, req)
	r.Observe(name, err)
	return resp, err
}

// Observe feeds a call outcome to the provider's breaker: only retryable
// failures (outages, overload) count; a rejected request says nothing about
// the provider's health.
func (r *Registry) Observe(name string, err error) {
	if err != nil && ctxErr(err) {
		return
	}
	r.breakers.Observe(name, err != nil && provider.Retryable(err))
}

func ctxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
