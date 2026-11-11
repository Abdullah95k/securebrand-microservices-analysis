// Package pipeline turns queued items into stored analyses: it groups a
// thread's items into one request with the post as context, routes them to
// the configured model (real time or provider batch), applies the budget,
// falls back to another provider when the primary is down or refuses,
// validates answers, and writes results with escalation and audit
// follow-ups.
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/ametrics"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/entities"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/prompt"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider/registry"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/taxonomy"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/textnorm"
)

// Pipeline is the analysis engine shared by the lanes.
type Pipeline struct {
	Store     *store.Store
	Providers *registry.Registry
	Cfg       config.Analysis
	Pricing   provider.Pricing
	Calib     map[string]*Calibration
	Budget    *Budget

	entities atomic.Pointer[entities.Registry]
	// down records since when a provider's batch API has been failing
	// (cleared by a successful submission).
	downMu sync.Mutex
	down   map[string]time.Time

	// tax is the topic list in use, with the prompts and schemas built
	// from it; the dynamic topic list swaps in new versions.
	tax atomic.Pointer[taxState]
	hot *HotAccounts
	now func() time.Time
}

// New builds a pipeline.
func New(st *store.Store, providers *registry.Registry, tax *taxonomy.Taxonomy, cfg config.Analysis, calib map[string]*Calibration) *Pipeline {
	p := &Pipeline{Store: st, Providers: providers, Cfg: cfg, Pricing: provider.Pricing(cfg.Pricing), Calib: calib,
		now: time.Now, down: map[string]time.Time{}}
	p.SetTaxonomy(tax)
	var watchlists func(context.Context) ([]string, error)
	if cfg.Urgent.Watchlists && st != nil {
		watchlists = st.HotSources
	}
	p.hot = NewHotAccounts(append(append([]string{}, cfg.Urgent.HotAccounts...), cfg.Urgent.Sources...), watchlists, cfg.Urgent.Refresh)
	routeCaps := map[string]float64{}
	for name, r := range cfg.Routes {
		routeCaps[name] = r.DailyUSD
	}
	p.Budget = &Budget{Store: st, DailyUSD: cfg.Budget.DailyUSD, RouteUSD: routeCaps, UrgentExempt: cfg.Budget.UrgentExempt}
	p.entities.Store(entities.New(nil, nil, nil))
	return p
}

// taxState is a topic list with its prompts and schemas.
type taxState struct {
	tax                    *taxonomy.Taxonomy
	textSystem             string
	textSchema, postSchema map[string]any // post route: with keywords
	// The schemas' sizes in JSON, for the cost estimate.
	textSchemaLen, postSchemaLen int
	mediaSystem                  string
	mediaSchema                  map[string]any
}

// schemaLen is a schema's size in JSON (0 if it can't be encoded).
func schemaLen(s map[string]any) int {
	b, err := json.Marshal(s)
	if err != nil {
		return 0
	}
	return len(b)
}

// SetTaxonomy swaps in a topic list (a new version of the dynamic list).
// Prompts and schemas are rebuilt only when the version changes, so the
// cached system prompt stays byte-identical between versions.
func (p *Pipeline) SetTaxonomy(t *taxonomy.Taxonomy) {
	if cur := p.tax.Load(); cur != nil && cur.tax.Version == t.Version {
		return
	}
	ts := &taxState{tax: t, textSystem: prompt.TextSystem(t), textSchema: prompt.TextSchema(t, false),
		postSchema: prompt.TextSchema(t, true), mediaSystem: prompt.MediaSystem(t), mediaSchema: prompt.MediaSchema(t)}
	ts.textSchemaLen, ts.postSchemaLen = schemaLen(ts.textSchema), schemaLen(ts.postSchema)
	p.tax.Store(ts)
}

// Taxonomy returns the topic list in use.
func (p *Pipeline) Taxonomy() *taxonomy.Taxonomy { return p.tax.Load().tax }

// MediaPrompt returns the media system prompt and schema of the topic list
// in use, and the list itself.
func (p *Pipeline) MediaPrompt() (string, map[string]any, *taxonomy.Taxonomy) {
	st := p.tax.Load()
	return st.mediaSystem, st.mediaSchema, st.tax
}

// SetEntities swaps in a new registry snapshot.
func (p *Pipeline) SetEntities(r *entities.Registry) { p.entities.Store(r) }

// Entities returns the current registry snapshot.
func (p *Pipeline) Entities() *entities.Registry { return p.entities.Load() }

// Plan decides an ingested item's route, lane and priority. Urgent items
// go to the real-time lane (results within minutes): those from a hot-tier
// account (HotAccounts), and those mentioning an entity a client tracks.
// Everything else takes its route's lane: the half-price batch lane for
// posts and comments.
func (p *Pipeline) Plan(it *core.Item) store.Plan {
	route := core.RouteFor(it, p.Cfg.Grouping.PostShortChars)
	lane := p.Cfg.Routes[route].Lane
	if lane == "" {
		lane = core.LaneBatch
	}
	pl := store.Plan{Route: route, Lane: lane, Priority: core.PriorityNormal}
	if p.hot.Hot(it.SourceID) || (p.Cfg.Urgent.TrackedEntities && p.trackedHit(it.Text)) {
		pl.Lane, pl.Priority = core.LaneRealtime, core.PriorityUrgent
	}
	return pl
}

// routing says why an item takes its lane, as the alert engine reads it:
// urgent_source (a hot-tier account), tracked_entity, or normal; and the
// tracked entities its text mentions.
func (p *Pipeline) routing(it *core.Item) (string, []string) {
	var ids []string
	seen := map[string]bool{}
	for _, m := range p.Entities().Find(it.Text) {
		if m.Tracked && !m.Ambiguous && !seen[m.EntityID] {
			seen[m.EntityID] = true
			ids = append(ids, m.EntityID)
		}
	}
	switch {
	case p.hot.Hot(it.SourceID):
		return "urgent_source", ids
	case p.Cfg.Urgent.TrackedEntities && len(ids) > 0:
		return "tracked_entity", ids
	}
	return "normal", ids
}

func (p *Pipeline) trackedHit(text string) bool {
	for _, m := range p.Entities().Find(text) {
		if m.Tracked && !m.Ambiguous {
			return true
		}
	}
	return false
}

// group is the items of one request, in prompt order.
type group struct {
	route  string
	items  []core.Item
	urgent bool
}

// Groups packs claimed items into requests: a thread's items together (up
// to grouping.max_items and max_chars), items retried after a failure
// alone, so one bad item cannot sink a group twice. An item identical to
// one already packed for its thread joins that request as a follower
// (dedup.go) and counts toward neither limit.
func (p *Pipeline) Groups(route string, cs []store.Claimed) []group {
	maxItems, maxChars := p.Cfg.Grouping.MaxItems, p.Cfg.Grouping.MaxChars
	byThread := map[string][]store.Claimed{}
	var order []string
	var out []group
	for _, c := range cs {
		if c.Attempts >= 2 {
			out = append(out, group{route: route, items: []core.Item{c.Item}, urgent: c.Priority == core.PriorityUrgent})
			continue
		}
		key := c.Item.ThreadID
		switch {
		case route == core.RoutePost:
			key = "" // long posts pack together, each with its page
		case c.Item.Kind == core.KindPost:
			key = "\x00post:" + c.Item.SourceID // short or escalated posts: by page, for the page context
		}
		if _, ok := byThread[key]; !ok {
			order = append(order, key)
		}
		byThread[key] = append(byThread[key], c)
	}
	for _, key := range order {
		var g group
		chars := 0
		// Where each leader of this thread went: the index in out, or -1
		// while its group is still being filled.
		leader := map[string]int{}
		var open []string
		flush := func() {
			out = append(out, g)
			for _, k := range open {
				leader[k] = len(out) - 1
			}
			g, chars, open = group{}, 0, nil
		}
		for _, c := range byThread[key] {
			urgent := c.Priority == core.PriorityUrgent
			k := ""
			if dedupRoute(route) && c.Item.Kind != core.KindPost {
				k = dupKey(&c.Item, c.Item.InputHash())
				if at, ok := leader[k]; ok {
					if at < 0 {
						g.items = append(g.items, c.Item)
						g.urgent = g.urgent || urgent
					} else {
						out[at].items = append(out[at].items, c.Item)
						out[at].urgent = out[at].urgent || urgent
					}
					continue
				}
			}
			n := len([]rune(c.Item.Text))
			if len(g.items) > 0 && (len(open) >= maxItems || chars+n > maxChars) {
				flush()
			}
			g.route = route
			g.items = append(g.items, c.Item)
			g.urgent = g.urgent || urgent
			chars += n
			if k != "" && c.Item.Kind != core.KindPost {
				leader[k] = -1
			}
			open = append(open, k)
		}
		if len(g.items) > 0 {
			flush()
		}
	}
	return out
}

// built is a request ready to send.
type built struct {
	req    provider.Request
	items  []core.Item
	hashes [][]byte
	// prompted counts the items shown to the model (the leaders).
	prompted int
	// schemaChars is the size of the request's schema in JSON (the
	// estimate prices it with the system prompt).
	schemaChars int
}

func (p *Pipeline) mask(s string) string {
	if p.Cfg.Privacy.MaskPII {
		return textnorm.MaskPII(s)
	}
	return s
}

// Outbound is text as it may leave the service: the names of tagged
// people hidden (privacy.mask_tagged_users; tagged registry entities such
// as brands stay), cut to limit runes (0 = whole), then phones and e-mails
// masked (privacy.mask_pii).
func (p *Pipeline) Outbound(text string, tags []textnorm.Span, limit int) string {
	if p.Cfg.Privacy.MaskTaggedUsers {
		text = textnorm.MaskSpans(text, tags, textnorm.TagPlaceholder, p.Entities().Known)
	}
	if limit > 0 {
		text = textnorm.Truncate(text, limit)
	}
	return p.mask(text)
}

func hints(ms []entities.Match) []prompt.Hint {
	out := make([]prompt.Hint, 0, len(ms))
	for _, m := range ms {
		out = append(out, prompt.Hint{ID: m.EntityID, Name: m.Name, Ambiguous: m.Ambiguous})
	}
	return out
}

// build renders a group's request: the thread's post and page as context
// (comment routes), each item's text (masked), its alias hints and its
// parent comment.
func (p *Pipeline) build(ctx context.Context, g group, model string) (built, error) {
	reg := p.Entities()
	b := built{items: g.items}
	for _, it := range g.items {
		b.hashes = append(b.hashes, it.InputHash())
	}
	lead := leaders(g.route, g.items, b.hashes)
	ts := p.tax.Load()
	var c prompt.Context
	inGroup := map[string]int{}
	for i, it := range g.items {
		inGroup[it.ContentID] = lead[i]
	}
	var inputs []prompt.ItemInput
	if g.route != core.RoutePost {
		var need []string
		threads := map[string]bool{}
		for _, it := range g.items {
			if it.ThreadID != "" && !threads[it.ThreadID] {
				threads[it.ThreadID] = true
				need = append(need, it.ThreadID)
			}
			if it.Kind == core.KindReply && it.ParentID != "" && it.ParentID != it.ThreadID {
				if _, ok := inGroup[it.ParentID]; !ok {
					need = append(need, it.ParentID)
				}
			}
		}
		ctxItems, err := p.Store.ItemsByID(ctx, need)
		if err != nil {
			return b, err
		}
		if len(g.items) > 0 {
			if post, ok := ctxItems[g.items[0].ThreadID]; ok {
				c.Post = p.Outbound(post.Text, post.RemoveSpans, p.Cfg.Grouping.ContextChars)
				c.Page = post.SourceName
			}
		}
		if c.Page == "" && len(g.items) > 0 && g.items[0].Kind == core.KindPost {
			c.Page = g.items[0].SourceName // posts are grouped by page
		}
		if c.Page != "" {
			c.PageHints = hints(reg.Find(c.Page))
		}
		for i, it := range g.items {
			if lead[i] != i {
				continue // a follower: gets its leader's answer
			}
			in := prompt.ItemInput{I: i, Kind: it.Kind, Text: p.Outbound(it.Text, it.RemoveSpans, 0), ReplyTo: -1, Hints: hints(reg.Find(it.Text))}
			if it.Kind == core.KindReply {
				if j, ok := inGroup[it.ParentID]; ok {
					in.ReplyTo = j
				} else if par, ok := ctxItems[it.ParentID]; ok {
					in.ParentText = p.Outbound(par.Text, par.RemoveSpans, 300)
				}
			}
			inputs = append(inputs, in)
		}
		b.req.User = prompt.TextUser(c, inputs)
		b.req.Schema, b.schemaChars = ts.textSchema, ts.textSchemaLen
		if allPosts(g.items) && g.route != core.RouteComment {
			// escalated or audited long posts keep the model's keywords
			b.req.Schema, b.schemaChars = ts.postSchema, ts.postSchemaLen
		}
	} else {
		for i, it := range g.items {
			if lead[i] == i {
				inputs = append(inputs, prompt.ItemInput{I: i, Kind: it.Kind, Text: p.Outbound(it.Text, it.RemoveSpans, 0), ReplyTo: -1, Hints: hints(reg.Find(it.Text))})
			}
		}
		page := ""
		if len(g.items) > 0 {
			page = g.items[0].SourceName
		}
		b.req.User = prompt.TextUser(prompt.Context{Page: page}, inputs)
		b.req.Schema, b.schemaChars = ts.postSchema, ts.postSchemaLen
	}
	b.prompted = len(inputs)
	rc := p.Cfg.Routes[g.route]
	b.req.Model = model
	b.req.System = ts.textSystem
	b.req.SchemaName = "items"
	b.req.MaxTokens = rc.MaxTokens
	b.req.Effort = rc.Effort
	b.req.Thinking = rc.Thinking
	return b, nil
}

func allPosts(items []core.Item) bool {
	for _, it := range items {
		if it.Kind != core.KindPost {
			return false
		}
	}
	return len(items) > 0
}

// estimate is the expected cost of a built request: its system prompt and
// schema, its items, and their expected output.
func (p *Pipeline) estimate(b built, batch bool) float64 {
	return p.Pricing.Estimate(b.req.Model, len(b.req.System)+b.schemaChars, len(b.req.User), len(b.req.Images), 60*b.prompted, batch)
}

// sent identifies a request being answered.
type sent struct {
	id       uuid.UUID
	route    string
	lane     string
	provider string
	model    string
	fallback bool
	urgent   bool
}

// handle validates an answer and stores it: valid items written (with
// escalation and audit follow-ups), missing or invalid ones released for a
// retry alone, a refusal of one item sent to the fallback or stored as
// refused.
func (p *Pipeline) handle(ctx context.Context, s sent, items []core.Item, hashes [][]byte, resp *provider.Response) error {
	cost := p.Pricing.Cost(s.model, resp.Usage, s.lane == core.LaneBatch)
	p.Budget.Add(s.route, cost)
	p.account(s, resp, cost)
	out := store.RequestOutcome{ID: s.id, Provider: s.provider, Model: s.model, Fallback: s.fallback,
		Usage:   [4]int{resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.CacheReadTokens, resp.Usage.CacheWriteTokens},
		CostUSD: cost, LatencyMS: int(resp.Latency.Milliseconds()), MaxAttempts: p.Cfg.Limits.MaxAttempts}
	// Followers share their leader's answer (dedup.go).
	lead := leaders(s.route, items, hashes)
	var want []int
	for i := range items {
		if lead[i] == i {
			want = append(want, i)
		}
	}
	var results []store.ItemResult
	switch {
	case resp.Refused && len(want) == 1:
		ametrics.Refusals.WithLabelValues(s.route, s.model).Inc()
		if fb, ok := p.fallbackFor(s); ok {
			// One refused input: the fallback provider gets one chance.
			resp2, err := p.Providers.Complete(ctx, fb.provider, p.rebuildFor(ctx, s, items, fb.model))
			if err == nil && !resp2.Refused {
				fb.id = s.id
				return p.handle(ctx, fb, items, hashes, resp2)
			}
		}
		per := cost / float64(len(items))
		for i := range items {
			results = append(results, p.result(s, items[i], hashes[i], core.Result{Refused: true, Sentiment: core.SentimentNeutral,
				Lang: "other", Dialect: "none", Script: "none", Intent: "other", Emotion: "none", Toxicity: "none"}, per))
		}
		out.Reason = "refused"
	case resp.Refused:
		ametrics.Refusals.WithLabelValues(s.route, s.model).Inc()
		ametrics.ItemsFailed.WithLabelValues(s.route, "refused").Add(float64(len(items)))
		out.Failed, out.Reason = ids(items), "refused: retried one by one"
	default:
		got, missing, probs, err := prompt.ParseText(resp.Text, want, p.Taxonomy(), p.Entities())
		p.repairs(s.route, probs)
		if err != nil {
			reason := "invalid"
			if resp.Truncated {
				reason = "truncated"
			}
			ametrics.ItemsFailed.WithLabelValues(s.route, reason).Add(float64(len(items)))
			out.Failed, out.Reason = ids(items), reason+": "+err.Error()
			break
		}
		failed := 0
		answered := 0
		for i := range items {
			if _, ok := got[lead[i]]; ok {
				answered++
			} else {
				out.Failed = append(out.Failed, items[i].ContentID)
				failed++
			}
		}
		if len(missing) > 0 {
			ametrics.ItemsFailed.WithLabelValues(s.route, "missing").Add(float64(failed))
			out.Reason = fmt.Sprintf("%d item(s) missing from the answer", failed)
		}
		per := cost / float64(max(answered, 1))
		for i := range items {
			r, ok := got[lead[i]]
			if !ok {
				continue
			}
			r.Entities = append([]core.Entity(nil), r.Entities...)
			prompt.Finish(&r, p.Outbound(items[i].Text, items[i].RemoveSpans, 0))
			results = append(results, p.result(s, items[i], hashes[i], r, per))
			if lead[i] != i {
				ametrics.Deduplicated.WithLabelValues(s.route).Inc()
			}
		}
	}
	if err := p.finishTopics(ctx, results); err != nil {
		return err
	}
	st, err := p.Store.WriteResults(ctx, results, out)
	if err != nil {
		return err
	}
	ametrics.Analyzed.WithLabelValues(s.route, s.model, "written").Add(float64(st.Written))
	ametrics.Analyzed.WithLabelValues(s.route, s.model, "stale").Add(float64(st.Stale))
	ametrics.Analyzed.WithLabelValues(s.route, s.model, "duplicate").Add(float64(st.Duplicate))
	p.candidates(ctx, results)
	return nil
}

// result builds a stored result: calibration, provenance, follow-ups.
func (p *Pipeline) result(s sent, it core.Item, hash []byte, r core.Result, cost float64) store.ItemResult {
	if c := p.Calib[s.model]; c != nil {
		r.CalibratedConfidence = c.Apply(r.Confidence)
	} else {
		r.CalibratedConfidence = r.Confidence
	}
	ir := store.ItemResult{Item: it, Route: s.route, InputHash: hash, Result: r, CostUSD: cost, Audit: s.route == core.RouteAudit,
		Prov: core.Provenance{Provider: s.provider, Model: s.model, PromptVersion: prompt.PromptVersion, TaxonomyVersion: p.Taxonomy().Version,
			SchemaVersion: prompt.SchemaVersion, Route: s.route, Lane: s.lane, Fallback: s.fallback, AnalyzedAt: p.now().UTC(),
			RequestID: s.id.String()}}
	ir.Prov.Routing, ir.Prov.MatchedEntityIDs = p.routing(&it)
	if s.route == core.RouteComment || s.route == core.RoutePost {
		if p.escalate(r) {
			ir.FollowUps = append(ir.FollowUps, core.RouteEscalation)
			ametrics.FollowUps.WithLabelValues(core.RouteEscalation).Inc()
		}
		if a, ok := p.Cfg.Routes[core.RouteAudit]; ok && a.On() && Sampled(it.ContentID, a.SampleRate) && !r.Refused {
			ir.FollowUps = append(ir.FollowUps, core.RouteAudit)
			ametrics.FollowUps.WithLabelValues(core.RouteAudit).Inc()
		}
	}
	return ir
}

// escalate: a tracked entity is mentioned and the result is unsure,
// sarcastic or mixed.
func (p *Pipeline) escalate(r core.Result) bool {
	esc, ok := p.Cfg.Routes[core.RouteEscalation]
	if !ok || !esc.On() || r.Refused {
		return false
	}
	reg := p.Entities()
	tracked := false
	for _, e := range r.Entities {
		if ent, ok := reg.Get(e.ID); ok && ent.Tracked {
			tracked = true
			break
		}
	}
	return tracked && (r.CalibratedConfidence < esc.MinConfidence || r.Sarcasm || r.Sentiment == core.SentimentMixed)
}

// Sampled selects a deterministic share of content ids.
func Sampled(contentID string, rate float64) bool {
	if rate <= 0 {
		return false
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(contentID))
	return float64(h.Sum32()%1_000_000) < rate*1_000_000
}

func (p *Pipeline) fallbackFor(s sent) (sent, bool) {
	rc := p.Cfg.Routes[s.route]
	if s.fallback || rc.FallbackProvider == "" || !p.Providers.Has(rc.FallbackProvider) {
		return sent{}, false
	}
	return sent{id: s.id, route: s.route, lane: core.LaneRealtime, provider: rc.FallbackProvider, model: rc.FallbackModel,
		fallback: true, urgent: s.urgent}, true
}

// rebuildFor rebuilds a request for another model (fallback).
func (p *Pipeline) rebuildFor(ctx context.Context, s sent, items []core.Item, model string) provider.Request {
	b, err := p.build(ctx, group{route: s.route, items: items}, model)
	if err != nil {
		slog.Warn("analysis: rebuild for fallback", "err", err)
	}
	b.req.ID = s.id.String()
	return b.req
}

func (p *Pipeline) account(s sent, resp *provider.Response, cost float64) {
	ametrics.Tokens.WithLabelValues(s.model, s.route, "input").Add(float64(resp.Usage.InputTokens))
	ametrics.Tokens.WithLabelValues(s.model, s.route, "output").Add(float64(resp.Usage.OutputTokens))
	ametrics.Tokens.WithLabelValues(s.model, s.route, "cache_read").Add(float64(resp.Usage.CacheReadTokens))
	ametrics.Tokens.WithLabelValues(s.model, s.route, "cache_write").Add(float64(resp.Usage.CacheWriteTokens))
	ametrics.CostUSD.WithLabelValues(s.model, s.route).Add(cost)
	if s.fallback {
		ametrics.Fallbacks.WithLabelValues(s.route).Inc()
	}
}

func (p *Pipeline) repairs(route string, pr prompt.Problems) {
	for k, v := range map[string]int{"invalid_topic": pr.InvalidTopics, "private_person": pr.PrivatePersons,
		"fixed_value": pr.FixedValues, "duplicate_item": pr.DuplicateItems, "unexpected_item": pr.UnexpectedItems} {
		if v > 0 {
			ametrics.Repairs.WithLabelValues(route, k).Add(float64(v))
		}
	}
}

// finishTopics completes the results' topic lists before they are stored:
// the model's proposals of missing topics resolved into topic ids
// (deduplicated against the list, new ones as candidates), then "@<id>"
// for every linked entity the item is about. Audits never add topics.
func (p *Pipeline) finishTopics(ctx context.Context, rs []store.ItemResult) error {
	if p.Cfg.Topics.Dynamic {
		props := make([][]core.TopicProposal, len(rs))
		for i := range rs {
			if !rs[i].Audit {
				props[i] = rs[i].Result.NewTopics
			}
		}
		reg := p.Entities()
		ids, err := p.Store.ResolveTopics(ctx, props, func(name string) string { return reg.Link("", name) })
		if err != nil {
			return err
		}
		for i := range rs {
			rs[i].Result.Topics = addTopics(rs[i].Result.Topics, ids[i]...)
		}
	}
	for i := range rs {
		rs[i].Result.Topics = addTopics(rs[i].Result.Topics, EntityTopics(rs[i].Result.Entities)...)
		rs[i].Result.NewTopics = nil
	}
	return nil
}

// EntityTopics are the "@<entity id>" topics of an item's linked entities
// (brands, organisations, public figures, positions, places, events,
// products, media outlets).
func EntityTopics(ents []core.Entity) []string {
	var out []string
	for _, e := range ents {
		if e.ID == "" || e.Type == "person_private" || e.Type == "other" {
			continue
		}
		out = append(out, "@"+e.ID)
	}
	return out
}

// addTopics appends topics not yet in the list, up to maxTopics in all.
func addTopics(list []string, add ...string) []string {
	for _, t := range add {
		if len(list) >= maxTopics {
			break
		}
		dup := false
		for _, x := range list {
			dup = dup || x == t
		}
		if !dup {
			list = append(list, t)
		}
	}
	return list
}

// maxTopics caps an item's topics: the model's (up to prompt.MaxTopics),
// new ones and entity topics.
const maxTopics = 24

// candidates sends unlinked entity names to the review queue (best effort).
func (p *Pipeline) candidates(ctx context.Context, rs []store.ItemResult) {
	var cs []store.Candidate
	reg := p.Entities()
	for _, r := range rs {
		for _, e := range r.Result.Entities {
			// A generic name ("رئيس الوزراء" meaning another country's) is
			// not a new entity to review.
			if e.ID == "" && e.Type != "person_private" && e.Type != "other" && !reg.Ambiguous(e.Name) {
				cs = append(cs, store.Candidate{Surface: e.Name, Type: e.Type, ContentID: r.Item.ContentID})
			}
		}
	}
	if err := p.Store.AddCandidates(ctx, cs); err != nil {
		slog.Warn("analysis: entity candidates", "err", err)
	}
}

func ids(items []core.Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ContentID
	}
	return out
}

// RunGroup analyses one group in real time: budget, primary (or the
// fallback when the primary's circuit is open or it fails), validation and
// storage.
func (p *Pipeline) RunGroup(ctx context.Context, g group, lane string) error {
	rc := p.Cfg.Routes[g.route]
	s := sent{id: uuid.New(), route: g.route, lane: core.LaneRealtime, provider: rc.Provider, model: rc.Model, urgent: g.urgent}
	if !p.Providers.Available(rc.Provider) {
		if fb, ok := p.fallbackFor(s); ok {
			s = fb
		}
	}
	b, err := p.build(ctx, g, s.model)
	if err != nil {
		return err
	}
	b.req.ID = s.id.String()
	est := p.estimate(b, false)
	if ok, err := p.Budget.Allow(ctx, g.route, est, g.urgent); err != nil || !ok {
		if err == nil {
			err = p.Store.ReleaseUnsent(ctx, ids(g.items), g.route, 10*time.Minute)
		}
		return err
	}
	if err := p.Store.StartRequest(ctx, store.RequestRow{ID: s.id, Route: g.route, Lane: core.LaneRealtime, Provider: s.provider,
		Model: s.model, Fallback: s.fallback, ContentIDs: ids(b.items), InputHashes: b.hashes, EstCostUSD: est}); err != nil {
		return err
	}
	start := p.now()
	resp, err := p.Providers.Complete(ctx, s.provider, b.req)
	if err != nil && provider.Retryable(err) {
		if fb, ok := p.fallbackFor(s); ok {
			ametrics.Requests.WithLabelValues(s.provider, s.model, lane, "error").Inc()
			s = fb
			resp, err = p.Providers.Complete(ctx, s.provider, p.rebuildFor(ctx, s, b.items, s.model))
		}
	}
	if err != nil {
		ametrics.Requests.WithLabelValues(s.provider, s.model, lane, "error").Inc()
		ametrics.ItemsFailed.WithLabelValues(g.route, "error").Add(float64(len(g.items)))
		if errors.Is(err, context.Canceled) {
			// Shutting down: close the request (its estimate must not count
			// against the budget) and return the items without an attempt.
			return p.Store.AbortRequests(context.WithoutCancel(ctx), []uuid.UUID{s.id}, "cancelled", 0)
		}
		return p.Store.FailRequest(ctx, s.id, err.Error(), p.Cfg.Limits.MaxAttempts)
	}
	ametrics.Requests.WithLabelValues(s.provider, s.model, lane, "ok").Inc()
	ametrics.RequestSecs.WithLabelValues(s.provider, s.model).Observe(p.now().Sub(start).Seconds())
	return p.handle(ctx, s, b.items, b.hashes, resp)
}

// QueueMetrics refreshes the queue gauges.
func (p *Pipeline) QueueMetrics(ctx context.Context) error {
	stats, err := p.Store.Stats(ctx)
	if err != nil {
		return err
	}
	ametrics.QueueDepth.Reset()
	ametrics.QueueAge.Reset()
	for _, q := range stats {
		ametrics.QueueDepth.WithLabelValues(q.Lane, q.Route).Set(float64(q.Depth))
		ametrics.QueueAge.WithLabelValues(q.Lane, q.Route).Set(q.OldestAge.Seconds())
	}
	n, err := p.Store.DeadLetterCount(ctx)
	if err != nil {
		return err
	}
	ametrics.DeadLetters.Set(float64(n))
	return nil
}

// RecordModels writes a series break for every route whose model, prompt
// or taxonomy changed since the last run.
func (p *Pipeline) RecordModels(ctx context.Context) error {
	names := make([]string, 0, len(p.Cfg.Routes))
	for n := range p.Cfg.Routes {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		r := p.Cfg.Routes[n]
		if !r.On() {
			continue
		}
		changed, err := p.Store.RecordModelChange(ctx, n, r.Model, prompt.PromptVersion, p.Taxonomy().Version)
		if err != nil {
			return err
		}
		if changed {
			slog.Info("analysis: route configuration recorded", "route", n, "model", r.Model, "prompt", prompt.PromptVersion,
				"taxonomy", p.Taxonomy().Version)
		}
	}
	return nil
}

// routeOrder lists the text routes the lanes serve, most urgent first.
var routeOrder = []string{core.RouteComment, core.RoutePost, core.RouteEscalation, core.RouteAudit}
