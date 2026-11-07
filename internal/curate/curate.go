// Package curate keeps the dynamic topic list clean: once a day it shows
// the candidate topics used by enough items to the curation model, which
// merges synonyms into existing topics, rejects names of people or
// companies and approves the rest; the result is a new taxonomy version.
package curate

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/ametrics"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/prompt"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider/registry"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/taxonomy"
)

// maxPerRun bounds the candidates shown in one call.
const maxPerRun = 100

// Curator runs the topic check.
type Curator struct {
	Store     *store.Store
	Providers *registry.Registry
	Pricing   provider.Pricing
	Cfg       config.AnalysisTopics
	// Taxonomy returns the active list.
	Taxonomy func() *taxonomy.Taxonomy
	// Allow, when set, checks the daily budget before a paid call: at the
	// cap, curation waits for the next run.
	Allow func(ctx context.Context, route string, est float64) (bool, error)
}

// allowed reports whether a curation call of about est USD fits today's
// budget.
func allowed(ctx context.Context, allow func(context.Context, string, float64) (bool, error), est float64) (bool, error) {
	if allow == nil {
		return true, nil
	}
	return allow(ctx, "curation", est)
}

// Run checks the candidates used by at least approve_min_items items and
// applies the decisions. Without a curation provider, candidates are
// approved on the count alone (their names were already deduplicated). A
// configured provider that is down defers the check to the next run.
// It returns the new taxonomy version ("" when the list is unchanged).
func (c *Curator) Run(ctx context.Context) (string, error) {
	rows, err := c.Store.Topics(ctx, store.TopicCandidate, c.Cfg.ApproveMinItems)
	if err != nil || len(rows) == 0 {
		return "", err
	}
	if len(rows) > maxPerRun {
		rows = rows[:maxPerRun]
	}
	var ds []store.TopicDecision
	if c.Cfg.Curation.Provider == "" {
		for _, r := range rows {
			ds = append(ds, store.TopicDecision{ID: r.ID, Action: "approve"})
		}
	} else {
		if !c.Providers.Available(c.Cfg.Curation.Provider) {
			slog.Warn("analysis: topic curation deferred, provider unavailable", "provider", c.Cfg.Curation.Provider)
			return "", nil
		}
		if ds, err = c.decide(ctx, rows); err != nil {
			return "", err
		}
	}
	if len(ds) == 0 {
		return "", nil
	}
	v, err := c.Store.ApplyTopicDecisions(ctx, ds, "curation")
	if err == nil && v != "" {
		slog.Info("analysis: topic list updated", "version", v, "decisions", len(ds))
	}
	return v, err
}

func (c *Curator) decide(ctx context.Context, rows []store.TopicRow) ([]store.TopicDecision, error) {
	tax := c.Taxonomy()
	cands := make([]prompt.CurationCandidate, len(rows))
	for i, r := range rows {
		cands[i] = prompt.CurationCandidate{ID: r.ID, EN: r.EN, AR: r.AR, Parent: r.Parent, Seen: r.Seen}
	}
	rc := c.Cfg.Curation
	req := provider.Request{ID: uuid.NewString(), Model: rc.Model, System: prompt.CurationSystem, User: prompt.CurationUser(tax, cands),
		Schema: prompt.CurationSchema(tax), SchemaName: "topic_decisions", MaxTokens: rc.MaxTokens, Effort: rc.Effort, Thinking: rc.Thinking}
	if ok, err := allowed(ctx, c.Allow, c.Pricing.Estimate(rc.Model, len(req.System), len(req.User), 0, 1000, false)); err != nil || !ok {
		if err == nil {
			slog.Info("analysis: topic curation deferred, daily budget reached")
		}
		return nil, err
	}
	start := time.Now()
	resp, err := c.Providers.Complete(ctx, rc.Provider, req)
	if err != nil {
		return nil, fmt.Errorf("topic curation: %w", err)
	}
	cost := c.Pricing.Cost(rc.Model, resp.Usage, false)
	c.record(ctx, req.ID, rc, resp, cost, time.Since(start))
	if resp.Refused {
		return nil, fmt.Errorf("topic curation: refused by %s", rc.Model)
	}
	parsed, err := prompt.ParseCuration(resp.Text, tax, cands)
	if err != nil {
		return nil, err
	}
	out := make([]store.TopicDecision, len(parsed))
	for i, d := range parsed {
		out[i] = store.TopicDecision{ID: d.ID, Action: d.Action, Into: d.Into, EN: d.EN, AR: d.AR, Parent: d.Parent}
	}
	return out, nil
}

// record logs the call as a provider request, so the day's spend includes
// it.
func (c *Curator) record(ctx context.Context, id string, rc config.Route, resp *provider.Response, cost float64, lat time.Duration) {
	rid, _ := uuid.Parse(id)
	if err := c.Store.StartRequest(ctx, store.RequestRow{ID: rid, Route: "curation", Lane: core.LaneRealtime, Provider: rc.Provider,
		Model: rc.Model, ContentIDs: []string{"topics:curation"}, InputHashes: [][]byte{{}}, EstCostUSD: cost}); err != nil {
		slog.Warn("analysis: curation request not recorded", "err", err)
		return
	}
	if _, err := c.Store.WriteResults(ctx, nil, store.RequestOutcome{ID: rid, Provider: rc.Provider, Model: rc.Model,
		Usage:   [4]int{resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.CacheReadTokens, resp.Usage.CacheWriteTokens},
		CostUSD: cost, LatencyMS: int(lat.Milliseconds())}); err != nil {
		slog.Warn("analysis: curation request not closed", "err", err)
	}
	ametrics.CostUSD.WithLabelValues(rc.Model, "curation").Add(cost)
}
