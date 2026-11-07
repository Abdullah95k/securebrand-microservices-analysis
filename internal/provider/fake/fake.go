// Package fake is a deterministic provider for tests and the local stack:
// it answers text requests from a small keyword lexicon and the request's
// entity hints, media requests with a fixed description, and supports
// batches and transcription. It never calls the network.
package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/textnorm"
)

// Provider is the fake provider. Hooks let tests inject behaviour.
type Provider struct {
	name string
	// Fail, when set, is called before answering; a non-nil error is
	// returned instead of an answer.
	Fail func(req provider.Request) error
	// Refuse, when set, makes the provider refuse matching requests.
	Refuse func(req provider.Request) bool
	// Drop, when set, leaves item i out of a text answer.
	Drop func(i int) bool
	// Transcribing, when set, runs inside Transcribe (tests hold a
	// transcription open with it); its error is returned.
	Transcribing func(ctx context.Context) error

	mu      sync.Mutex
	batches map[string][]provider.Request
	calls   int
	seq     int
}

// New creates a fake provider.
func New(name string) *Provider {
	if name == "" {
		name = "fake"
	}
	return &Provider{name: name, batches: map[string][]provider.Request{}}
}

// Name is the provider name.
func (p *Provider) Name() string { return p.name }

// Calls returns how many requests were answered (real time and batched).
func (p *Provider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// Complete answers one request.
func (p *Provider) Complete(_ context.Context, req provider.Request) (*provider.Response, error) {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	if p.Fail != nil {
		if err := p.Fail(req); err != nil {
			return nil, err
		}
	}
	r := &provider.Response{ID: req.ID, Model: req.Model, StopReason: "end_turn", Latency: time.Millisecond}
	if p.Refuse != nil && p.Refuse(req) {
		r.Refused, r.StopReason = true, "refusal"
		return r, nil
	}
	switch {
	case req.SchemaName == "topic_decisions":
		r.Text = curationAnswer(req)
	case req.SchemaName == "entity_decisions":
		r.Text = entityCurationAnswer(req)
	case len(req.Images) > 0 || req.SchemaName == "media":
		r.Text = mediaAnswer(req)
	default:
		r.Text = p.textAnswer(req)
	}
	r.Usage = provider.Usage{InputTokens: len(req.User) / 3, CacheReadTokens: len(req.System) / 3, OutputTokens: len(r.Text) / 3}
	return r, nil
}

type item struct {
	I     int    `json:"i"`
	Kind  string `json:"kind"`
	Text  string `json:"text"`
	Hints []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Ambiguous bool   `json:"ambiguous"`
	} `json:"hints"`
}

var (
	positive = []string{"شكرا", "حلو", "ممتاز", "احسن", "جميل", "عاشت", "رائع", "زين جدا", "good", "great", "love", "thanks"}
	negative = []string{"زباله", "سيء", "خايس", "فاشل", "ماكو", "انقطاع", "نصب", "حرامي", "bad", "worst", "scam", "terrible"}
)

func (p *Provider) textAnswer(req provider.Request) string {
	start := strings.Index(req.User, "<items>")
	end := strings.LastIndex(req.User, "</items>")
	var items []item
	if start >= 0 && end > start {
		_ = json.Unmarshal([]byte(strings.TrimSpace(req.User[start+len("<items>"):end])), &items)
	}
	out := []map[string]any{}
	kw := wantsKeywords(req.Schema)
	for _, it := range items {
		if p.Drop != nil && p.Drop(it.I) {
			continue
		}
		norm := textnorm.Normalize(it.Text)
		sent, score := "neutral", 0
		pos, neg := contains(norm, positive), contains(norm, negative)
		switch {
		case pos && neg:
			sent, score = "mixed", 0
		case pos:
			sent, score = "positive", 70
		case neg:
			sent, score = "negative", -70
		}
		intent := "other"
		switch {
		case neg:
			intent = "complaint"
		case pos:
			intent = "praise"
		case strings.Contains(it.Text, "?") || strings.Contains(it.Text, "؟"):
			intent = "question"
		}
		ents := []map[string]any{}
		for _, h := range it.Hints {
			if h.Ambiguous {
				// Like the model: a generic title without Iraqi context is
				// someone else, listed as written without the id.
				if strings.Contains(it.Text, "رئيس الوزراء") {
					ents = append(ents, map[string]any{"name": "رئيس الوزراء", "type": "position", "id": "", "sent": sent, "score": score, "aspect": "general"})
				}
				continue
			}
			ents = append(ents, map[string]any{"name": h.Name, "type": "brand", "id": h.ID, "sent": sent, "score": score, "aspect": "general"})
		}
		lng := "ar-iq"
		if norm == "" {
			lng = "none"
		} else if isLatin(norm) {
			lng = "en"
		}
		// Markers for tests: a missing topic, an ad, a scam.
		newTopics := []map[string]any{}
		if strings.Contains(it.Text, "شح المياه") {
			newTopics = append(newTopics, map[string]any{"en": "Water shortage", "ar": "شح المياه", "parent": "services"})
		}
		flags := []string{}
		if strings.Contains(it.Text, "إعلان") {
			flags = append(flags, "ad_paid")
		}
		if strings.Contains(norm, textnorm.Normalize("نصب")) {
			flags = append(flags, "fraud_scam")
		}
		ans := map[string]any{
			"i": it.I, "lng": lng, "sent": sent, "score": score, "conf": 80, "ents": ents, "topics": []string{"other"},
			"new_topics": newTopics, "intent": intent, "emotion": "none", "tox": "none", "flags": flags, "ppl": 0,
		}
		if kw {
			ans["kw"] = []string{"fake"}
		}
		out = append(out, ans)
	}
	b, _ := json.Marshal(map[string]any{"items": out})
	return string(b)
}

func mediaAnswer(req provider.Request) string {
	b, _ := json.Marshal(map[string]any{
		"logos": []any{}, "ocr": "", "desc": fmt.Sprintf("An image (%d attached).", len(req.Images)), "labels": []string{"image"},
		"people": 0, "sent": "neutral", "score": 0, "conf": 50, "ents": []any{}, "topics": []string{"other"}, "flags": []string{},
	})
	return string(b)
}

// curationAnswer approves every candidate of a topic curation request.
func curationAnswer(req provider.Request) string {
	var cands []struct {
		ID, EN, AR, Parent string
	}
	start, end := strings.Index(req.User, "<candidates>"), strings.LastIndex(req.User, "</candidates>")
	if start >= 0 && end > start {
		_ = json.Unmarshal([]byte(strings.TrimSpace(req.User[start+len("<candidates>"):end])), &cands)
	}
	ds := []map[string]any{}
	for _, c := range cands {
		ds = append(ds, map[string]any{"id": c.ID, "action": "approve", "into": "", "en": c.EN, "ar": c.AR, "parent": c.Parent})
	}
	b, _ := json.Marshal(map[string]any{"decisions": ds})
	return string(b)
}

// entityCurationAnswer approves every candidate whose name has a Latin
// letter (id from the name), merges "اسيا سيل" into asiacell, and rejects
// the rest (as a private person would be).
func entityCurationAnswer(req provider.Request) string {
	var cands []struct {
		Key, Name, Type string
	}
	start, end := strings.Index(req.User, "<candidates>"), strings.LastIndex(req.User, "</candidates>")
	if start >= 0 && end > start {
		_ = json.Unmarshal([]byte(strings.TrimSpace(req.User[start+len("<candidates>"):end])), &cands)
	}
	ds := []map[string]any{}
	for _, c := range cands {
		d := map[string]any{"key": c.Key, "action": "reject", "into": "", "id": "", "name": "", "name_ar": "", "type": "other", "public_figure": false}
		switch {
		case strings.Contains(c.Name, "اسيا"):
			d["action"], d["into"] = "merge", "asiacell"
		case isLatin(textnorm.Normalize(c.Name)):
			d["action"], d["id"], d["name"], d["type"] = "approve", strings.ReplaceAll(strings.ToLower(c.Name), " ", "-"), c.Name, c.Type
			d["public_figure"] = c.Type == "person_public"
		}
		ds = append(ds, d)
	}
	b, _ := json.Marshal(map[string]any{"decisions": ds})
	return string(b)
}

// wantsKeywords reports whether the request's schema asks for "kw" (the
// post route).
func wantsKeywords(schema map[string]any) bool {
	b, _ := json.Marshal(schema)
	return strings.Contains(string(b), `"kw":`)
}

func contains(norm string, words []string) bool {
	for _, w := range words {
		if strings.Contains(norm, textnorm.Normalize(w)) {
			return true
		}
	}
	return false
}

func isLatin(s string) bool {
	for _, r := range s {
		if r >= 0x0600 && r <= 0x06FF {
			return false
		}
	}
	return true
}

// SubmitBatch stores the requests; the batch is done at once.
func (p *Provider) SubmitBatch(_ context.Context, reqs []provider.Request) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seq++
	id := fmt.Sprintf("fakebatch_%d", p.seq)
	p.batches[id] = append([]provider.Request(nil), reqs...)
	return id, nil
}

// BatchDone is always true for known batches.
func (p *Provider) BatchDone(_ context.Context, id string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.batches[id]; !ok {
		return false, fmt.Errorf("fake: unknown batch %s", id)
	}
	return true, nil
}

// BatchResults answers every request of the batch.
func (p *Provider) BatchResults(ctx context.Context, id string) ([]provider.BatchResult, error) {
	p.mu.Lock()
	reqs, ok := p.batches[id]
	p.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("fake: unknown batch %s", id)
	}
	out := make([]provider.BatchResult, 0, len(reqs))
	for _, r := range reqs {
		resp, err := p.Complete(ctx, r)
		out = append(out, provider.BatchResult{ID: r.ID, Response: resp, Err: err})
	}
	return out, nil
}

// Transcribe returns a fixed transcript.
func (p *Provider) Transcribe(ctx context.Context, audio []byte, _, _ string) (string, error) {
	if p.Transcribing != nil {
		if err := p.Transcribing(ctx); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("نص تجريبي (%d bytes)", len(audio)), nil
}
