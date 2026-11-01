// Package taxonomy loads the versioned topic taxonomy
// (configs/analysis/taxonomy.v1.yaml).
package taxonomy

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/abdullah95k/securebrand-microservices/configs"
)

// Topic is one node of the taxonomy.
type Topic struct {
	ID       string  `yaml:"id"`
	EN       string  `yaml:"en"`
	AR       string  `yaml:"ar"`
	Children []Topic `yaml:"children"`
}

// Taxonomy is a loaded, validated taxonomy.
type Taxonomy struct {
	Version string  `yaml:"version"`
	Topics  []Topic `yaml:"topics"`
	ids     map[string]bool
}

// Load reads path, or the embedded default when path is "".
func Load(path string) (*Taxonomy, error) {
	var b []byte
	var err error
	if path == "" {
		b, err = configs.FS.ReadFile("analysis/taxonomy.v1.yaml")
	} else {
		b, err = os.ReadFile(path) // #nosec G304 -- operator-configured file
	}
	if err != nil {
		return nil, fmt.Errorf("taxonomy: %w", err)
	}
	return Parse(b)
}

// Parse validates a taxonomy document: a version, unique ids, children
// prefixed by their parent's id.
func Parse(b []byte) (*Taxonomy, error) {
	var t Taxonomy
	if err := yaml.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("taxonomy: %w", err)
	}
	return &t, t.validate()
}

// New builds a taxonomy from topics (the dynamic list in the database),
// with the same validation as Parse.
func New(version string, topics []Topic) (*Taxonomy, error) {
	t := Taxonomy{Version: version, Topics: topics}
	if err := t.validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

// MaxDepth is the deepest topic level: area, topic, sub-topic
// ("services", "services.electricity", "services.electricity.cuts").
const MaxDepth = 3

func (t *Taxonomy) validate() error {
	if t.Version == "" {
		return errors.New("taxonomy: version is required")
	}
	t.ids = map[string]bool{}
	var walk func(ts []Topic, parent string, depth int) error
	walk = func(ts []Topic, parent string, depth int) error {
		for _, c := range ts {
			if parent != "" && !strings.HasPrefix(c.ID, parent+".") {
				return fmt.Errorf("taxonomy: %s must start with %s and a dot", c.ID, parent)
			}
			if depth > MaxDepth {
				return fmt.Errorf("taxonomy: %s is deeper than %d levels", c.ID, MaxDepth)
			}
			if err := t.add(c.ID); err != nil {
				return err
			}
			if err := walk(c.Children, c.ID, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(t.Topics, "", 1); err != nil {
		return err
	}
	if len(t.ids) == 0 {
		return errors.New("taxonomy: no topics")
	}
	return nil
}

func (t *Taxonomy) add(id string) error {
	if id == "" {
		return errors.New("taxonomy: empty topic id")
	}
	if t.ids[id] {
		return fmt.Errorf("taxonomy: duplicate topic %s", id)
	}
	t.ids[id] = true
	return nil
}

// each visits every topic in document order with its depth (1 = area).
func (t *Taxonomy) each(fn func(Topic, int)) {
	var walk func(ts []Topic, depth int)
	walk = func(ts []Topic, depth int) {
		for _, c := range ts {
			fn(c, depth)
			walk(c.Children, depth+1)
		}
	}
	walk(t.Topics, 1)
}

// Get returns a topic (at any level) by id.
func (t *Taxonomy) Get(id string) (Topic, bool) {
	var out Topic
	found := false
	t.each(func(c Topic, _ int) {
		if !found && c.ID == id {
			out, found = c, true
		}
	})
	return out, found
}

// Valid reports whether id is a topic of this taxonomy.
func (t *Taxonomy) Valid(id string) bool { return t.ids[id] }

// Len is the number of topics (all levels).
func (t *Taxonomy) Len() int { return len(t.ids) }

// IDs lists every topic id in document order.
func (t *Taxonomy) IDs() []string {
	var out []string
	t.each(func(c Topic, _ int) { out = append(out, c.ID) })
	return out
}

// ParentIDs lists the ids a new topic may be placed under: areas and
// topics (not sub-topics, the deepest level).
func (t *Taxonomy) ParentIDs() []string {
	var out []string
	t.each(func(c Topic, depth int) {
		if depth < MaxDepth {
			out = append(out, c.ID)
		}
	})
	return out
}

// Depth is the level of a topic id (1 for an area).
func Depth(id string) int { return strings.Count(id, ".") + 1 }

// TopIDs lists the top-level topic ids in document order.
func (t *Taxonomy) TopIDs() []string {
	out := make([]string, 0, len(t.Topics))
	for _, top := range t.Topics {
		out = append(out, top.ID)
	}
	return out
}

// Prompt renders the taxonomy compactly for the system prompt: one line per
// topic, "id: English / Arabic", indented by level.
func (t *Taxonomy) Prompt() string {
	var b strings.Builder
	t.each(func(c Topic, depth int) {
		fmt.Fprintf(&b, "%s%s: %s / %s\n", strings.Repeat("  ", depth-1), c.ID, c.EN, c.AR)
	})
	return b.String()
}
