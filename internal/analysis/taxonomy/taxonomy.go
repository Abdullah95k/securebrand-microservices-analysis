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

func (t *Taxonomy) validate() error {
	if t.Version == "" {
		return errors.New("taxonomy: version is required")
	}
	t.ids = map[string]bool{}
	for _, top := range t.Topics {
		if err := t.add(top.ID); err != nil {
			return err
		}
		for _, c := range top.Children {
			if !strings.HasPrefix(c.ID, top.ID+".") {
				return fmt.Errorf("taxonomy: %s must start with %s and a dot", c.ID, top.ID)
			}
			if err := t.add(c.ID); err != nil {
				return err
			}
		}
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

// Get returns a topic (top-level or child) by id.
func (t *Taxonomy) Get(id string) (Topic, bool) {
	for _, top := range t.Topics {
		if top.ID == id {
			return top, true
		}
		for _, c := range top.Children {
			if c.ID == id {
				return c, true
			}
		}
	}
	return Topic{}, false
}

// Valid reports whether id is a topic of this taxonomy.
func (t *Taxonomy) Valid(id string) bool { return t.ids[id] }

// Len is the number of topics (top-level and children).
func (t *Taxonomy) Len() int { return len(t.ids) }

// IDs lists every topic id in document order.
func (t *Taxonomy) IDs() []string {
	var out []string
	for _, top := range t.Topics {
		out = append(out, top.ID)
		for _, c := range top.Children {
			out = append(out, c.ID)
		}
	}
	return out
}

// TopIDs lists the top-level topic ids in document order.
func (t *Taxonomy) TopIDs() []string {
	out := make([]string, 0, len(t.Topics))
	for _, top := range t.Topics {
		out = append(out, top.ID)
	}
	return out
}

// Prompt renders the taxonomy compactly for the system prompt: one line per
// topic, "id: English / Arabic".
func (t *Taxonomy) Prompt() string {
	var b strings.Builder
	for _, top := range t.Topics {
		fmt.Fprintf(&b, "%s: %s / %s\n", top.ID, top.EN, top.AR)
		for _, c := range top.Children {
			fmt.Fprintf(&b, "  %s: %s / %s\n", c.ID, c.EN, c.AR)
		}
	}
	return b.String()
}
