package prompt

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/entities"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/taxonomy"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/textnorm"
)

// Limits applied to every answer.
const (
	MaxTopics    = 3
	MaxKeywords  = 5
	MaxEntities  = 12
	maxNameRunes = 80
	maxKwRunes   = 40
	maxOCRRunes  = 500
	maxDescRunes = 300
	maxLabels    = 8
)

// ErrInvalidJSON means the answer is not the JSON object the schema asks
// for; the request is retried (then routed to the fallback).
var ErrInvalidJSON = errors.New("prompt: invalid JSON answer")

type entityOut struct {
	Name   string  `json:"name"`
	Type   string  `json:"type"`
	ID     string  `json:"id"`
	Sent   string  `json:"sent"`
	Score  float64 `json:"score"`
	Aspect string  `json:"aspect"`
}

type itemOut struct {
	I       *int        `json:"i"`
	Lang    string      `json:"lang"`
	Dialect string      `json:"dialect"`
	Script  string      `json:"script"`
	CS      bool        `json:"cs"`
	Sent    string      `json:"sent"`
	Score   float64     `json:"score"`
	Conf    float64     `json:"conf"`
	Ents    []entityOut `json:"ents"`
	Topics  []string    `json:"topics"`
	KW      []string    `json:"kw"`
	Intent  string      `json:"intent"`
	Emotion string      `json:"emotion"`
	Tox     string      `json:"tox"`
	Hate    bool        `json:"hate"`
	Threat  bool        `json:"threat"`
	Harass  bool        `json:"harass"`
	Spam    bool        `json:"spam"`
	Sarcasm bool        `json:"sarcasm"`
	Urgent  bool        `json:"urgent"`
	PPL     int         `json:"ppl"`
}

// Problems counts what validation had to repair or drop, for metrics.
type Problems struct {
	InvalidTopics   int
	PrivatePersons  int
	FixedValues     int
	DuplicateItems  int
	UnexpectedItems int
}

// ExtractJSON returns the outermost JSON object of an answer, tolerating
// code fences and text around it (some OpenAI-compatible servers add them).
func ExtractJSON(raw string) (string, error) {
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start < 0 || end <= start {
		return "", ErrInvalidJSON
	}
	return raw[start : end+1], nil
}

// ParseText validates a text answer for the requested item indexes. It
// returns the valid results by index and the indexes that are missing or
// unusable (they are retried alone).
func ParseText(raw string, want []int, tax *taxonomy.Taxonomy, reg *entities.Registry) (map[int]core.Result, []int, Problems, error) {
	var p Problems
	js, err := ExtractJSON(raw)
	if err != nil {
		return nil, want, p, err
	}
	var doc struct {
		Items []itemOut `json:"items"`
	}
	if err := json.Unmarshal([]byte(js), &doc); err != nil {
		return nil, want, p, fmt.Errorf("%w: %w", ErrInvalidJSON, err)
	}
	wanted := map[int]bool{}
	for _, i := range want {
		wanted[i] = true
	}
	out := map[int]core.Result{}
	for _, o := range doc.Items {
		if o.I == nil || !wanted[*o.I] {
			p.UnexpectedItems++
			continue
		}
		if _, dup := out[*o.I]; dup {
			p.DuplicateItems++
			continue
		}
		if !oneOf(o.Sent, Sentiments) {
			continue // unusable: retried
		}
		out[*o.I] = toResult(o, tax, reg, &p)
	}
	var missing []int
	for _, i := range want {
		if _, ok := out[i]; !ok {
			missing = append(missing, i)
		}
	}
	return out, missing, p, nil
}

func toResult(o itemOut, tax *taxonomy.Taxonomy, reg *entities.Registry, p *Problems) core.Result {
	r := core.Result{
		Lang:       pick(o.Lang, Langs, "other", p),
		Dialect:    pick(o.Dialect, Dialects, "none", p),
		Script:     pick(o.Script, Scripts, "none", p),
		CodeSwitch: o.CS,
		Sentiment:  o.Sent,
		Score:      clamp(o.Score, -1, 1, p),
		Confidence: clamp(o.Conf, 0, 1, p),
		Intent:     pick(o.Intent, Intents, "other", p),
		Emotion:    pick(o.Emotion, Emotions, "none", p),
		Toxicity:   pick(o.Tox, Toxicities, "none", p),
		Hate:       o.Hate,
		Threat:     o.Threat,
		Harassment: o.Harass,
		Spam:       o.Spam,
		Sarcasm:    o.Sarcasm,
		Urgent:     o.Urgent,
	}
	r.CalibratedConfidence = r.Confidence
	r.PrivatePersonMentions = max(o.PPL, 0)
	r.Topics = Topics(o.Topics, tax, p)
	for _, k := range o.KW {
		k = strings.TrimSpace(k)
		if k == "" || len(r.Keywords) >= MaxKeywords {
			continue
		}
		r.Keywords = append(r.Keywords, cut(k, maxKwRunes))
	}
	r.Entities, r.PrivatePersonMentions = Entities(o.Ents, reg, r.PrivatePersonMentions, p)
	return r
}

// Topics keeps valid taxonomy ids, without duplicates, at most MaxTopics.
func Topics(in []string, tax *taxonomy.Taxonomy, p *Problems) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range in {
		if !tax.Valid(t) {
			p.InvalidTopics++
			continue
		}
		if seen[t] || len(out) >= MaxTopics {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// Entities validates entity mentions: private persons are dropped (only
// counted), ids are linked through the registry, duplicates merged.
func Entities(in []entityOut, reg *entities.Registry, ppl int, p *Problems) ([]core.Entity, int) {
	var out []core.Entity
	seen := map[string]bool{}
	for _, e := range in {
		name := strings.TrimSpace(e.Name)
		typ := e.Type
		if !oneOf(typ, EntityTypes) {
			typ = "other"
			p.FixedValues++
		}
		if typ == "person_private" {
			p.PrivatePersons++
			ppl++
			continue
		}
		if name == "" && e.ID == "" {
			continue
		}
		id := reg.Link(e.ID, name)
		if id != "" {
			if ent, ok := reg.Get(id); ok {
				name = ent.Name
				if ent.Type == "person_private" {
					ppl++
					continue
				}
			}
		}
		key := id
		if key == "" {
			key = typ + ":" + textnorm.Normalize(name)
		}
		if seen[key] || len(out) >= MaxEntities {
			continue
		}
		seen[key] = true
		sent := e.Sent
		if !oneOf(sent, Sentiments) {
			sent = core.SentimentNeutral
			p.FixedValues++
		}
		aspect := e.Aspect
		if !oneOf(aspect, Aspects) {
			aspect = "other"
		}
		out = append(out, core.Entity{ID: id, Name: cut(name, maxNameRunes), Type: typ, Sentiment: sent,
			Score: clamp(e.Score, -1, 1, p), Aspect: aspect, Origin: "text"})
	}
	return out, ppl
}

// ParseMedia validates an image or video answer.
func ParseMedia(raw string, tax *taxonomy.Taxonomy, reg *entities.Registry) (core.MediaResult, Problems, error) {
	var p Problems
	js, err := ExtractJSON(raw)
	if err != nil {
		return core.MediaResult{}, p, err
	}
	var o struct {
		Logos []struct {
			Name string  `json:"name"`
			ID   string  `json:"id"`
			Conf float64 `json:"conf"`
		} `json:"logos"`
		OCR    string      `json:"ocr"`
		Desc   string      `json:"desc"`
		Labels []string    `json:"labels"`
		People int         `json:"people"`
		Sent   string      `json:"sent"`
		Score  float64     `json:"score"`
		Conf   float64     `json:"conf"`
		Ents   []entityOut `json:"ents"`
		Topics []string    `json:"topics"`
	}
	if err := json.Unmarshal([]byte(js), &o); err != nil {
		return core.MediaResult{}, p, fmt.Errorf("%w: %w", ErrInvalidJSON, err)
	}
	r := core.MediaResult{
		TextInImage: cut(strings.TrimSpace(o.OCR), maxOCRRunes),
		Description: cut(strings.TrimSpace(o.Desc), maxDescRunes),
		PeopleCount: max(o.People, 0),
		Sentiment:   pick(o.Sent, Sentiments, core.SentimentNeutral, &p),
		Score:       clamp(o.Score, -1, 1, &p),
		Confidence:  clamp(o.Conf, 0, 1, &p),
		Topics:      Topics(o.Topics, tax, &p),
	}
	for _, l := range o.Labels {
		if l = strings.TrimSpace(l); l != "" && len(r.Labels) < maxLabels {
			r.Labels = append(r.Labels, cut(l, maxKwRunes))
		}
	}
	seen := map[string]bool{}
	for _, l := range o.Logos {
		name := strings.TrimSpace(l.Name)
		if name == "" {
			continue
		}
		id := reg.Link(l.ID, name)
		if id != "" {
			if e, ok := reg.Get(id); ok {
				name = e.Name
			}
		}
		key := id
		if key == "" {
			key = textnorm.Normalize(name)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		r.Logos = append(r.Logos, core.Logo{ID: id, Name: cut(name, maxNameRunes), Confidence: clamp(l.Conf, 0, 1, &p)})
	}
	r.Entities, _ = Entities(o.Ents, reg, 0, &p)
	return r, p, nil
}

func oneOf(v string, vals []string) bool {
	for _, x := range vals {
		if x == v {
			return true
		}
	}
	return false
}

func pick(v string, vals []string, def string, p *Problems) string {
	if oneOf(v, vals) {
		return v
	}
	p.FixedValues++
	return def
}

func clamp(v, lo, hi float64, p *Problems) float64 {
	if math.IsNaN(v) {
		p.FixedValues++
		return 0
	}
	if v < lo {
		p.FixedValues++
		return lo
	}
	if v > hi {
		p.FixedValues++
		return hi
	}
	return v
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
