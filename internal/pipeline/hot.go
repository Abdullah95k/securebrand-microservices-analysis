package pipeline

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// HotAccounts says whether an item's account (its source, e.g.
// "fb:page:123") is in the hot tier, whose items are analysed in real time.
// Until the platform's account registry publishes tiers
// (registry.account_request.v1, with the contract in REQ-022), the hot tier
// is analysis.urgent.hot_accounts (and the older urgent.sources), plus the
// accounts clients monitor (analysis.client_sources: client watchlists put
// their accounts in the hot tier), re-read every refresh.
type HotAccounts struct {
	fixed map[string]bool
	// load reads the watchlists; nil = the config list only.
	load  func(context.Context) ([]string, error)
	every time.Duration
	now   func() time.Time

	mu       sync.Mutex
	set      map[string]bool
	loadedAt time.Time
}

// NewHotAccounts builds the hot tier from fixed account ids and, when load
// is set, the watchlists it reads at most every refresh.
func NewHotAccounts(fixed []string, load func(context.Context) ([]string, error), refresh time.Duration) *HotAccounts {
	h := &HotAccounts{fixed: map[string]bool{}, load: load, every: refresh, now: time.Now}
	for _, s := range fixed {
		h.fixed[s] = true
	}
	return h
}

// Hot reports whether source is in the hot tier. A failed read of the
// watchlists keeps the last set.
func (h *HotAccounts) Hot(source string) bool {
	if h == nil || source == "" {
		return false
	}
	if h.fixed[source] {
		return true
	}
	if h.load == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.loadedAt.IsZero() || h.now().Sub(h.loadedAt) >= h.every {
		h.loadedAt = h.now()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		ids, err := h.load(ctx)
		cancel()
		if err != nil {
			slog.Warn("analysis: watchlists not read; keeping the last hot accounts", "err", err)
		} else {
			set := make(map[string]bool, len(ids))
			for _, id := range ids {
				set[id] = true
			}
			h.set = set
		}
	}
	return h.set[source]
}
