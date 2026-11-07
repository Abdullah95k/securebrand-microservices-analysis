package curate

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/entities"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/prompt"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider/registry"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/textnorm"
)

// EntityCurator grows the entity registry without a person: names the
// analysis model found often enough (brands, organisations, public
// figures, positions, places...) go to the curation model with example
// texts and the most similar registry entities; it merges spellings into
// known entities, approves new ones and rejects private persons and
// generic words. The owner can still decide any candidate by hand.
type EntityCurator struct {
	Store     *store.Store
	Providers *registry.Registry
	Pricing   provider.Pricing
	Route     config.Route
	MinSeen   int
	// Registry returns the current registry snapshot.
	Registry func() *entities.Registry
	MaskPII  bool
	// MaskTags hides the names of tagged people in the examples
	// (analysis.privacy.mask_tagged_users).
	MaskTags bool
	// Allow, when set, checks the daily budget before a paid call (as
	// Curator.Allow).
	Allow func(ctx context.Context, route string, est float64) (bool, error)
}

// Run decides the pending candidates seen at least MinSeen times; it
// returns how many it decided. A curation provider that is down defers
// the run.
func (c *EntityCurator) Run(ctx context.Context) (int, error) {
	if c.Route.Provider == "" {
		return 0, nil
	}
	rows, err := c.Store.PendingCandidates(ctx, c.MinSeen, maxPerRun)
	if err != nil || len(rows) == 0 {
		return 0, err
	}
	if !c.Providers.Available(c.Route.Provider) {
		slog.Warn("analysis: entity curation deferred, provider unavailable", "provider", c.Route.Provider)
		return 0, nil
	}
	reg := c.Registry()
	var ids []string
	for _, r := range rows {
		ids = append(ids, firstN(r.Examples, 2)...)
	}
	texts, err := c.Store.ItemsByID(ctx, ids)
	if err != nil {
		return 0, err
	}
	cands := make([]prompt.EntityCandidate, len(rows))
	byKey := map[string]store.CandidateRow{}
	for i, r := range rows {
		key := fmt.Sprintf("%d", i)
		byKey[key] = r
		cand := prompt.EntityCandidate{Key: key, Name: r.Surface, Type: r.Type, Seen: r.Seen, Similar: reg.Similar(r.Surface, 3)}
		for _, id := range firstN(r.Examples, 2) {
			if it, ok := texts[id]; ok && it.Text != "" {
				t := it.Text
				if c.MaskTags {
					t = textnorm.MaskSpans(t, it.RemoveSpans, textnorm.TagPlaceholder, reg.Known)
				}
				t = textnorm.Truncate(t, 200)
				if c.MaskPII {
					t = textnorm.MaskPII(t)
				}
				cand.Examples = append(cand.Examples, t)
			}
		}
		cands[i] = cand
	}
	req := provider.Request{ID: uuid.NewString(), Model: c.Route.Model, System: prompt.EntityCurationSystem,
		User: prompt.EntityCurationUser(cands), Schema: prompt.EntityCurationSchema(), SchemaName: "entity_decisions",
		MaxTokens: c.Route.MaxTokens, Effort: c.Route.Effort, Thinking: c.Route.Thinking}
	if ok, err := allowed(ctx, c.Allow, c.Pricing.Estimate(c.Route.Model, len(req.System), len(req.User), 0, 1000, false)); err != nil || !ok {
		if err == nil {
			slog.Info("analysis: entity curation deferred, daily budget reached")
		}
		return 0, err
	}
	start := time.Now()
	resp, err := c.Providers.Complete(ctx, c.Route.Provider, req)
	if err != nil {
		return 0, fmt.Errorf("entity curation: %w", err)
	}
	cost := c.Pricing.Cost(c.Route.Model, resp.Usage, false)
	(&Curator{Store: c.Store}).record(ctx, req.ID, c.Route, resp, cost, time.Since(start))
	if resp.Refused {
		return 0, fmt.Errorf("entity curation: refused by %s", c.Route.Model)
	}
	ds, err := prompt.ParseEntityCuration(resp.Text, cands, func(id string) bool { _, ok := reg.Get(id); return ok })
	if err != nil {
		return 0, err
	}
	n := 0
	for _, d := range ds {
		r := byKey[d.Key]
		err := c.Store.DecideCandidate(ctx, r.Norm, r.Type, store.EntityDecision{Action: d.Action, Into: d.Into, ID: d.ID, Name: d.Name,
			NameAR: d.NameAR, Type: d.Type, PublicFigure: d.PublicFigure, By: c.Route.Model})
		if err != nil {
			slog.Warn("analysis: entity decision not applied", "candidate", r.Surface, "action", d.Action, "err", err)
			continue
		}
		n++
	}
	if n > 0 {
		slog.Info("analysis: entity registry updated", "decisions", n)
	}
	return n, nil
}

func firstN(xs []string, n int) []string {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}
