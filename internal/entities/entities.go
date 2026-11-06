// Package entities is the entity registry seen by the pipeline: canonical
// entities, their aliases in Arabic, English and Arabizi, which ones clients
// track, an alias matcher that finds them in text before any model is
// called, and the linking of model-reported names to registry ids.
package entities

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/abdullah95k/securebrand-microservices-analysis/configs"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/textnorm"
)

// Entity types the registry and the model use.
var Types = []string{"brand", "product", "organization", "person_public", "person_private", "position", "place", "event", "media_outlet", "other"}

// Entity is a canonical entity.
type Entity struct {
	ID           string
	Name         string
	NameAR       string
	Type         string
	PublicFigure bool
	// Tracked: at least one client tracks it (own brand, competitor, watch).
	Tracked bool
}

// Alias links a spelling to an entity. An ambiguous alias is a generic
// name that can mean other entities too ("رئيس الوزراء" is any country's
// prime minister): it is offered to the model as a hint marked ambiguous,
// but never links a name on its own and never makes an item urgent.
type Alias struct {
	Alias     string
	EntityID  string
	Ambiguous bool
}

// Registry is an immutable snapshot; the service swaps in a new one when
// the registry changes.
type Registry struct {
	entities map[string]*Entity
	// merged maps a merged entity to the one it was merged into.
	merged  map[string]string
	aliases map[string][]string // normalised alias -> entity ids
	// ambiguous marks (normalised alias, entity id) pairs that are generic.
	ambiguous map[string]bool
	maxN      int
}

func pairKey(norm, id string) string { return norm + "\x00" + id }

// New builds a registry snapshot. merged maps merged ids to their target.
func New(ents []Entity, aliases []Alias, merged map[string]string) *Registry {
	r := &Registry{entities: map[string]*Entity{}, merged: merged, aliases: map[string][]string{}, ambiguous: map[string]bool{}}
	if r.merged == nil {
		r.merged = map[string]string{}
	}
	for i := range ents {
		e := ents[i]
		r.entities[e.ID] = &e
	}
	add := func(alias, id string, ambiguous bool) {
		norm := textnorm.Normalize(alias)
		if norm == "" {
			return
		}
		if ambiguous {
			r.ambiguous[pairKey(norm, id)] = true
		}
		for _, x := range r.aliases[norm] {
			if x == id {
				return
			}
		}
		r.aliases[norm] = append(r.aliases[norm], id)
		if n := len(strings.Fields(norm)); n > r.maxN {
			r.maxN = n
		}
	}
	for _, e := range ents {
		add(e.Name, e.ID, false)
		if e.NameAR != "" {
			add(e.NameAR, e.ID, false)
		}
	}
	for _, a := range aliases {
		if _, ok := r.entities[a.EntityID]; ok {
			add(a.Alias, a.EntityID, a.Ambiguous)
		}
	}
	return r
}

// Ambiguous reports whether a name is only a generic alias of registry
// entities (no unambiguous alias of any entity has that spelling).
func (r *Registry) Ambiguous(name string) bool {
	if r == nil {
		return false
	}
	found := false
	for _, k := range nameKeys(name) {
		for _, id := range r.aliases[k] {
			if !r.ambiguous[pairKey(k, id)] {
				return false
			}
			found = true
		}
	}
	return found
}

// Known reports whether name, as a whole, is an alias of a registry entity
// that is neither only ambiguous nor a private person. Tagged accounts with
// such names (brands, public figures) stay readable when tags are hidden.
func (r *Registry) Known(name string) bool {
	if r == nil {
		return false
	}
	for _, k := range nameKeys(name) {
		for _, id := range r.aliases[k] {
			if r.ambiguous[pairKey(k, id)] {
				continue
			}
			if e, ok := r.entities[id]; ok && e.Type != "person_private" {
				return true
			}
		}
	}
	return false
}

// nameKeys are the alias keys a name can match: normalised, and with a
// leading clitic removed from its first word.
func nameKeys(name string) []string {
	norm := textnorm.Normalize(name)
	keys := []string{norm}
	if f := strings.Fields(norm); len(f) > 0 {
		if s := textnorm.StripClitic(f[0]); s != "" {
			keys = append(keys, strings.Join(append([]string{s}, f[1:]...), " "))
		}
	}
	return keys
}

// Get returns an entity, following merges.
func (r *Registry) Get(id string) (*Entity, bool) {
	for i := 0; i < 8 && id != ""; i++ {
		if to, ok := r.merged[id]; ok {
			id = to
			continue
		}
		e, ok := r.entities[id]
		return e, ok
	}
	return nil, false
}

// Similar returns up to n entities whose name or aliases resemble name
// (word trigram similarity), most similar first, as "id: Name / الاسم".
func (r *Registry) Similar(name string, n int) []string {
	if r == nil {
		return nil
	}
	q := textnorm.Tokens(textnorm.Normalize(name))
	if len(q) == 0 {
		return nil
	}
	best := map[string]float64{}
	for alias, ids := range r.aliases {
		at := strings.Fields(alias)
		score := 0.0
		for _, w := range q {
			m := 0.0
			for _, a := range at {
				m = max(m, textnorm.Similarity(w, a))
			}
			score += m
		}
		score /= float64(max(len(q), len(at)))
		for _, id := range ids {
			if e, ok := r.Get(id); ok && score > best[e.ID] {
				best[e.ID] = score
			}
		}
	}
	type hit struct {
		id    string
		score float64
	}
	var hits []hit
	for id, sc := range best {
		if sc >= 0.3 {
			hits = append(hits, hit{id, sc})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].id < hits[j].id
	})
	var out []string
	for _, h := range hits {
		if len(out) == n {
			break
		}
		e := r.entities[h.id]
		out = append(out, fmt.Sprintf("%s: %s / %s", e.ID, e.Name, e.NameAR))
	}
	return out
}

// Len is the number of entities.
func (r *Registry) Len() int { return len(r.entities) }

// Match is an entity found in a text by one of its aliases. Ambiguous:
// only generic aliases matched (the model decides from the context).
type Match struct {
	EntityID  string
	Name      string
	Tracked   bool
	Ambiguous bool
}

// Find returns the registry entities whose aliases occur in text, matching
// whole words after normalisation and also after removing a leading Arabic
// clitic (و، ب، ل، ال، بال...) from the alias's first word.
func (r *Registry) Find(text string) []Match {
	if r == nil || len(r.aliases) == 0 {
		return nil
	}
	toks := textnorm.Tokens(textnorm.Normalize(text))
	at := map[string]int{}
	var out []Match
	hit := func(key string) {
		for _, id := range r.aliases[key] {
			e, ok := r.Get(id)
			if !ok {
				continue
			}
			amb := r.ambiguous[pairKey(key, id)]
			if i, ok := at[e.ID]; ok {
				// An unambiguous alias anywhere in the text settles it.
				out[i].Ambiguous = out[i].Ambiguous && amb
				continue
			}
			at[e.ID] = len(out)
			out = append(out, Match{EntityID: e.ID, Name: e.Name, Tracked: e.Tracked, Ambiguous: amb})
		}
	}
	for i := range toks {
		first := []string{toks[i]}
		if s := textnorm.StripClitic(toks[i]); s != "" {
			first = append(first, s)
		}
		for _, f := range first {
			key := f
			for n := 1; n <= r.maxN && i+n <= len(toks); n++ {
				if n > 1 {
					key += " " + toks[i+n-1]
				}
				hit(key)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EntityID < out[j].EntityID })
	return out
}

// Link resolves a model-reported entity to a registry id: a hinted or
// returned id that exists (following merges), else an exact unambiguous
// alias of its name (a generic name such as "رئيس الوزراء" links only
// through the id the model chose from the context). It returns "" when the
// registry does not know the entity.
func (r *Registry) Link(id, name string) string {
	if r == nil {
		return ""
	}
	if id != "" {
		if e, ok := r.Get(id); ok {
			return e.ID
		}
	}
	for _, k := range nameKeys(name) {
		if ids := r.aliases[k]; len(ids) == 1 && !r.ambiguous[pairKey(k, ids[0])] {
			if e, ok := r.Get(ids[0]); ok {
				return e.ID
			}
		}
	}
	return ""
}

// Seed is the YAML seed of the registry (configs/analysis/entities.seed.yaml).
type Seed struct {
	Entities []SeedEntity `yaml:"entities"`
}

// SeedEntity is one seeded entity with its aliases.
type SeedEntity struct {
	ID           string   `yaml:"id"`
	Name         string   `yaml:"name"`
	NameAR       string   `yaml:"name_ar"`
	Type         string   `yaml:"type"`
	PublicFigure bool     `yaml:"public_figure"`
	Aliases      []string `yaml:"aliases"`
	// Ambiguous lists generic names (they may also be the Arabic name):
	// hints for the model, never a link on their own.
	Ambiguous []string `yaml:"ambiguous"`
}

// LoadSeed reads path, or the embedded seed when path is "".
func LoadSeed(path string) (*Seed, error) {
	var b []byte
	var err error
	if path == "" {
		b, err = configs.FS.ReadFile("analysis/entities.seed.yaml")
	} else {
		b, err = os.ReadFile(path) // #nosec G304 -- operator-named file
	}
	if err != nil {
		return nil, fmt.Errorf("entities: %w", err)
	}
	var s Seed
	if err := yaml.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("entities: %w", err)
	}
	ids := map[string]bool{}
	for _, e := range s.Entities {
		if e.ID == "" || e.Name == "" {
			return nil, fmt.Errorf("entities: entity without id or name: %+v", e)
		}
		if ids[e.ID] {
			return nil, fmt.Errorf("entities: duplicate id %s", e.ID)
		}
		ids[e.ID] = true
		if !validType(e.Type) || e.Type == "person_private" {
			return nil, fmt.Errorf("entities: %s: type %q not allowed", e.ID, e.Type)
		}
	}
	return &s, nil
}

func validType(t string) bool {
	for _, x := range Types {
		if x == t {
			return true
		}
	}
	return false
}
