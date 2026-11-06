package prompt

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/entities"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/taxonomy"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/textnorm"
)

// Limits applied to every answer.
const (
	MaxTopics     = 10
	MaxNewTopics  = 3
	MaxKeywords   = 5
	MaxEntities   = 12
	maxNameRunes  = 80
	maxKwRunes    = 40
	maxOCRRunes   = 500
	maxDescRunes  = 300
	maxLabels     = 8
	maxTopicRunes = 60
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
	Lng     string      `json:"lng"`
	Sent    string      `json:"sent"`
	Score   float64     `json:"score"`
	Conf    float64     `json:"conf"`
	Ents    []entityOut `json:"ents"`
	Topics  []string    `json:"topics"`
	New     []newTopic  `json:"new_topics"`
	KW      []string    `json:"kw"`
	Intent  string      `json:"intent"`
	Emotion string      `json:"emotion"`
	Tox     string      `json:"tox"`
	Flags   []string    `json:"flags"`
	PPL     int         `json:"ppl"`
}

type newTopic struct {
	EN     string `json:"en"`
	AR     string `json:"ar"`
	Parent string `json:"parent"`
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
	lang, dialect, ok := LangOf(o.Lng)
	if !ok {
		p.FixedValues++
	}
	r := core.Result{
		Lang:       lang,
		Dialect:    dialect,
		Script:     "none", // set from the text by Finish
		Sentiment:  o.Sent,
		Score:      clamp(percent(o.Score), -1, 1, p),
		Confidence: clamp(percent(o.Conf), 0, 1, p),
		Intent:     pick(o.Intent, Intents, "other", p),
		Emotion:    pick(o.Emotion, Emotions, "none", p),
		Toxicity:   pick(o.Tox, Toxicities, "none", p),
	}
	for _, f := range o.Flags {
		switch f {
		case "cs":
			r.CodeSwitch = true
		case "sarcasm":
			r.Sarcasm = true
		case "spam":
			r.Spam = true
		case "hate":
			r.Hate = true
		case "threat":
			r.Threat = true
		case "harass":
			r.Harassment = true
		case "urgent":
			r.Urgent = true
		default:
			if !legalOrAd(f, &r.Legal, &r.AdType) {
				p.FixedValues++
			}
		}
	}
	// Hate speech and threats are breaches too.
	if r.Hate {
		r.Legal = append([]string{"hate_speech"}, r.Legal...)
	}
	if r.Threat {
		r.Legal = append(r.Legal, "threat")
	}
	r.NewTopics = NewTopics(o.New, tax)
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

// adRank orders ad types when the model lists several: the most specific
// wins (affiliate, then undisclosed, then paid).
var adRank = map[string]int{"affiliate": 5, "hidden": 4, "paid": 3, "own": 2, "sale": 1}

// legalOrAd records an ad or legal flag; false for an unknown flag.
func legalOrAd(f string, legal *[]string, ad *string) bool {
	if oneOf(f, AdFlags) {
		if t := strings.TrimPrefix(f, "ad_"); adRank[t] > adRank[*ad] {
			*ad = t
		}
		return true
	}
	if oneOf(f, LegalFlags) {
		if !oneOf(f, *legal) {
			*legal = append(*legal, f)
		}
		return true
	}
	return false
}

// NewTopics keeps well-formed proposals (a name, a top-level parent), at
// most MaxNewTopics, names trimmed.
func NewTopics(in []newTopic, tax *taxonomy.Taxonomy) []core.TopicProposal {
	var out []core.TopicProposal
	for _, n := range in {
		en, ar := cut(strings.TrimSpace(n.EN), maxTopicRunes), cut(strings.TrimSpace(n.AR), maxTopicRunes)
		if en == "" && ar == "" || len(out) >= MaxNewTopics {
			continue
		}
		parent := n.Parent
		if !oneOf(parent, tax.ParentIDs()) {
			// A sub-topic named as parent: place it next to it instead.
			if i := strings.LastIndexByte(parent, '.'); i > 0 && oneOf(parent[:i], tax.ParentIDs()) {
				parent = parent[:i]
			} else {
				parent = "other"
			}
		}
		out = append(out, core.TopicProposal{EN: en, AR: ar, Parent: parent})
	}
	return out
}

// percent turns a whole-number score (-100..100) or confidence (0..100)
// into the stored -1..1 / 0..1 scale. A fraction with a decimal part is
// taken as already scaled (a server that ignores the schema may answer
// 0.8); whole numbers, including 1, are percentages.
func percent(v float64) float64 {
	if v != math.Trunc(v) && math.Abs(v) <= 1 {
		return v
	}
	return v / 100
}

// Finish completes a text result with what the service computes itself
// from the item's text (the text as sent, contact details masked): the
// script, and keywords when the route does not ask the model for them.
func Finish(r *core.Result, text string) {
	r.Script = textnorm.Script(text)
	if len(r.Keywords) == 0 && !r.Refused {
		r.Keywords = textnorm.Keywords(text, MaxKeywords)
	}
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
			Score: clamp(percent(e.Score), -1, 1, p), Aspect: aspect, Origin: "text"})
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
		Flags  []string    `json:"flags"`
	}
	if err := json.Unmarshal([]byte(js), &o); err != nil {
		return core.MediaResult{}, p, fmt.Errorf("%w: %w", ErrInvalidJSON, err)
	}
	r := core.MediaResult{
		TextInImage: cut(strings.TrimSpace(o.OCR), maxOCRRunes),
		Description: cut(strings.TrimSpace(o.Desc), maxDescRunes),
		PeopleCount: max(o.People, 0),
		Sentiment:   pick(o.Sent, Sentiments, core.SentimentNeutral, &p),
		Score:       clamp(percent(o.Score), -1, 1, &p),
		Confidence:  clamp(percent(o.Conf), 0, 1, &p),
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
		r.Logos = append(r.Logos, core.Logo{ID: id, Name: cut(name, maxNameRunes), Confidence: clamp(percent(l.Conf), 0, 1, &p)})
	}
	r.Entities, _ = Entities(o.Ents, reg, 0, &p)
	for _, f := range o.Flags {
		if !legalOrAd(f, &r.Legal, &r.AdType) {
			p.FixedValues++
		}
	}
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
