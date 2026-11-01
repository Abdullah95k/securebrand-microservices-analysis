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

	"github.com/abdullah95k/securebrand-microservices/configs"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/textnorm"
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

// Alias links a spelling to an entity.
type Alias struct {
	Alias    string
	EntityID string
}

// Registry is an immutable snapshot; the service swaps in a new one when
// the registry changes.
type Registry struct {
	entities map[string]*Entity
	// merged maps a merged entity to the one it was merged into.
	merged  map[string]string
	aliases map[string][]string // normalised alias -> entity ids
	maxN    int
}

// New builds a registry snapshot. merged maps merged ids to their target.
func New(ents []Entity, aliases []Alias, merged map[string]string) *Registry {
	r := &Registry{entities: map[string]*Entity{}, merged: merged, aliases: map[string][]string{}}
	if r.merged == nil {
		r.merged = map[string]string{}
	}
	for i := range ents {
		e := ents[i]
		r.entities[e.ID] = &e
	}
	add := func(alias, id string) {
		norm := textnorm.Normalize(alias)
		if norm == "" {
			return
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
		add(e.Name, e.ID)
		if e.NameAR != "" {
			add(e.NameAR, e.ID)
		}
	}
	for _, a := range aliases {
		if _, ok := r.entities[a.EntityID]; ok {
			add(a.Alias, a.EntityID)
		}
	}
	return r
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

// Len is the number of entities.
func (r *Registry) Len() int { return len(r.entities) }

// Match is an entity found in a text by one of its aliases.
type Match struct {
	EntityID string
	Name     string
	Tracked  bool
}

// Find returns the registry entities whose aliases occur in text, matching
// whole words after normalisation and also after removing a leading Arabic
// clitic (و، ب، ل، ال، بال...) from the alias's first word.
func (r *Registry) Find(text string) []Match {
	if r == nil || len(r.aliases) == 0 {
		return nil
	}
	toks := textnorm.Tokens(textnorm.Normalize(text))
	seen := map[string]bool{}
	var out []Match
	hit := func(key string) {
		for _, id := range r.aliases[key] {
			e, ok := r.Get(id)
			if !ok || seen[e.ID] {
				continue
			}
			seen[e.ID] = true
			out = append(out, Match{EntityID: e.ID, Name: e.Name, Tracked: e.Tracked})
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
// returned id that exists (following merges), else an exact alias of its
// name. It returns "" when the registry does not know the entity.
func (r *Registry) Link(id, name string) string {
	if r == nil {
		return ""
	}
	if id != "" {
		if e, ok := r.Get(id); ok {
			return e.ID
		}
	}
	norm := textnorm.Normalize(name)
	try := []string{norm}
	if f := strings.Fields(norm); len(f) > 0 {
		if s := textnorm.StripClitic(f[0]); s != "" {
			try = append(try, strings.Join(append([]string{s}, f[1:]...), " "))
		}
	}
	for _, k := range try {
		if ids := r.aliases[k]; len(ids) == 1 {
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
