package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/entities"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/media"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/prompt"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/provider"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/taxonomy"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/textnorm"
)

// RunConfig is one model configuration under evaluation.
type RunConfig struct {
	Provider  string
	Model     string
	GroupSize int // items per request (grouped by post, as in production)
	Effort    string
	Thinking  string
	MaxTokens int
	Batch     bool // through the provider's batch API
	MaskPII   bool
	// Parallel real-time requests (default 4).
	Parallel int
}

// Request is one request's record in a results file.
type Request struct {
	Provider  string         `json:"provider"`
	Model     string         `json:"model"`
	Lane      string         `json:"lane"`
	GroupSize int            `json:"group_size"`
	Thinking  string         `json:"thinking"`
	Effort    string         `json:"effort"`
	Usage     provider.Usage `json:"usage"`
	CostUSD   float64        `json:"cost_usd"`
	LatencyMS int64          `json:"latency_ms"`
	Refused   bool           `json:"refused"`
	Truncated bool           `json:"truncated"`
	Error     string         `json:"error,omitempty"`
	Items     []Prediction   `json:"items"`
}

// Prediction is one item's outcome.
type Prediction struct {
	ID      string            `json:"id"`
	Result  *core.Result      `json:"result,omitempty"`
	Media   *core.MediaResult `json:"media,omitempty"`
	Missing bool              `json:"missing,omitempty"`
}

// Runner analyses gold items with a provider through the production prompt
// and validation code.
type Runner struct {
	Providers interface {
		Complete(ctx context.Context, name string, req provider.Request) (*provider.Response, error)
		Batcher(name string) (provider.Batcher, bool)
	}
	Tax      *taxonomy.Taxonomy
	Entities *entities.Registry
	Pricing  provider.Pricing
	Out      *json.Encoder
	// Poll is the batch polling interval.
	Poll time.Duration
	mu   sync.Mutex // guards Out
}

type group struct {
	items []Item
	req   provider.Request
}

// groups packs text items by post (same context), up to size items.
func (r *Runner) groups(items []Item, cfg RunConfig) []group {
	size := max(cfg.GroupSize, 1)
	byCtx := map[string][]Item{}
	var order []string
	for _, it := range items {
		if it.Kind == "image" {
			continue
		}
		k := it.Page + "\x00" + it.Post
		if it.Kind == core.KindPost {
			k = "\x01post"
		}
		if _, ok := byCtx[k]; !ok {
			order = append(order, k)
		}
		byCtx[k] = append(byCtx[k], it)
	}
	sys := prompt.TextSystem(r.Tax)
	commentSchema, postSchema := prompt.TextSchema(r.Tax, false), prompt.TextSchema(r.Tax, true)
	mask := func(s string) string {
		if cfg.MaskPII {
			return textnorm.MaskPII(s)
		}
		return s
	}
	var out []group
	n := 0
	for _, k := range order {
		list := byCtx[k]
		for start := 0; start < len(list); start += size {
			chunk := list[start:min(start+size, len(list))]
			c := prompt.Context{Page: chunk[0].Page}
			schema := postSchema
			if chunk[0].Kind != core.KindPost {
				c.Post = mask(textnorm.Truncate(chunk[0].Post, 1500))
				schema = commentSchema
			}
			for _, h := range r.Entities.Find(c.Page) {
				c.PageHints = append(c.PageHints, prompt.Hint{ID: h.EntityID, Name: h.Name, Ambiguous: h.Ambiguous})
			}
			inputs := make([]prompt.ItemInput, len(chunk))
			for i, it := range chunk {
				in := prompt.ItemInput{I: i, Kind: it.Kind, Text: mask(it.Text), ReplyTo: -1, ParentText: mask(textnorm.Truncate(it.Parent, 300))}
				for _, h := range r.Entities.Find(it.Text) {
					in.Hints = append(in.Hints, prompt.Hint{ID: h.EntityID, Name: h.Name, Ambiguous: h.Ambiguous})
				}
				inputs[i] = in
			}
			n++
			out = append(out, group{items: chunk, req: provider.Request{ID: fmt.Sprintf("eval-%d", n), Model: cfg.Model, System: sys,
				User: prompt.TextUser(c, inputs), Schema: schema, SchemaName: "items", MaxTokens: maxTok(cfg, len(chunk)),
				Effort: cfg.Effort, Thinking: cfg.Thinking}})
		}
	}
	return out
}

func maxTok(cfg RunConfig, n int) int {
	if cfg.MaxTokens > 0 {
		return cfg.MaxTokens
	}
	return 400 + 150*n
}

// Run analyses the items and writes one Request record per request.
func (r *Runner) Run(ctx context.Context, items []Item, cfg RunConfig) error {
	gs := r.groups(items, cfg)
	if cfg.Batch {
		if b, ok := r.Providers.Batcher(cfg.Provider); ok {
			if err := r.runBatch(ctx, b, gs, cfg); err != nil {
				return err
			}
			return r.runImages(ctx, items, cfg)
		}
		return fmt.Errorf("provider %s has no batch API configured", cfg.Provider)
	}
	work := make(chan group)
	var wg sync.WaitGroup
	for range max(cfg.Parallel, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for g := range work {
				start := time.Now()
				resp, err := r.Providers.Complete(ctx, cfg.Provider, g.req)
				r.record(g, cfg, resp, err, time.Since(start), "realtime")
			}
		}()
	}
	for _, g := range gs {
		work <- g
	}
	close(work)
	wg.Wait()
	return r.runImages(ctx, items, cfg)
}

func (r *Runner) runBatch(ctx context.Context, b provider.Batcher, gs []group, cfg RunConfig) error {
	if len(gs) == 0 {
		return nil
	}
	reqs := make([]provider.Request, len(gs))
	byID := map[string]group{}
	for i, g := range gs {
		reqs[i] = g.req
		byID[g.req.ID] = g
	}
	start := time.Now()
	id, err := b.SubmitBatch(ctx, reqs)
	if err != nil {
		return err
	}
	poll := r.Poll
	if poll <= 0 {
		poll = 30 * time.Second
	}
	for {
		done, err := b.BatchDone(ctx, id)
		if err != nil {
			return err
		}
		if done {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
	res, err := b.BatchResults(ctx, id)
	if err != nil {
		return err
	}
	elapsed := time.Since(start)
	for _, br := range res {
		g, ok := byID[br.ID]
		if !ok {
			continue
		}
		r.record(g, cfg, br.Response, br.Err, elapsed, "batch")
	}
	return nil
}

func (r *Runner) record(g group, cfg RunConfig, resp *provider.Response, err error, lat time.Duration, lane string) {
	rec := Request{Provider: cfg.Provider, Model: cfg.Model, Lane: lane, GroupSize: cfg.GroupSize, Thinking: cfg.Thinking,
		Effort: cfg.Effort, LatencyMS: lat.Milliseconds()}
	want := make([]int, len(g.items))
	for i := range want {
		want[i] = i
	}
	switch {
	case err != nil:
		rec.Error = err.Error()
	case resp.Refused:
		rec.Refused = true
	}
	var got map[int]core.Result
	if err == nil && resp != nil {
		rec.Usage, rec.Truncated = resp.Usage, resp.Truncated
		rec.CostUSD = r.Pricing.Cost(cfg.Model, resp.Usage, lane == "batch")
		if !resp.Refused {
			var perr error
			got, _, _, perr = prompt.ParseText(resp.Text, want, r.Tax, r.Entities)
			if perr != nil {
				rec.Error = perr.Error()
			}
		}
	}
	for i, it := range g.items {
		p := Prediction{ID: it.ID}
		if res, ok := got[i]; ok {
			res := res
			text := it.Text
			if cfg.MaskPII {
				text = textnorm.MaskPII(text)
			}
			prompt.Finish(&res, text)
			p.Result = &res
		} else {
			p.Missing = true
		}
		rec.Items = append(rec.Items, p)
	}
	r.mu.Lock()
	_ = r.Out.Encode(rec)
	r.mu.Unlock()
}

// runImages analyses image items one by one (real time).
func (r *Runner) runImages(ctx context.Context, items []Item, cfg RunConfig) error {
	sys, schema := prompt.MediaSystem(r.Tax), prompt.MediaSchema(r.Tax)
	for _, it := range items {
		if it.Kind != "image" || it.ImagePath == "" {
			continue
		}
		data, err := os.ReadFile(it.ImagePath) // #nosec G304 -- gold file paths on the owner's server
		if err != nil {
			return err
		}
		img, err := media.Resize(data, 1024)
		if err != nil {
			return fmt.Errorf("%s: %w", it.ID, err)
		}
		req := provider.Request{ID: it.ID, Model: cfg.Model, System: sys, User: prompt.MediaUser(it.Post, nil, "", 1),
			Images: []provider.Image{{MediaType: "image/jpeg", Data: img}}, Schema: schema, SchemaName: "media",
			MaxTokens: 1500, Effort: cfg.Effort, Thinking: cfg.Thinking}
		start := time.Now()
		resp, err := r.Providers.Complete(ctx, cfg.Provider, req)
		rec := Request{Provider: cfg.Provider, Model: cfg.Model, Lane: "realtime", GroupSize: 1, Thinking: cfg.Thinking,
			Effort: cfg.Effort, LatencyMS: time.Since(start).Milliseconds()}
		p := Prediction{ID: it.ID, Missing: true}
		if err != nil {
			rec.Error = err.Error()
		} else {
			rec.Usage, rec.Refused = resp.Usage, resp.Refused
			rec.CostUSD = r.Pricing.Cost(cfg.Model, resp.Usage, false)
			if !resp.Refused {
				if m, _, perr := prompt.ParseMedia(resp.Text, r.Tax, r.Entities); perr == nil {
					p.Media, p.Missing = &m, false
				} else {
					rec.Error = perr.Error()
				}
			}
		}
		rec.Items = []Prediction{p}
		_ = r.Out.Encode(rec)
	}
	return nil
}

// sortedKeys returns map keys in order (stable reports).
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
