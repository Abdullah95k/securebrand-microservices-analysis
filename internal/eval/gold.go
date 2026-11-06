// Package eval is the evaluation harness: the gold-set format, metrics
// (macro-F1 with bootstrap intervals, aspect-sentiment, entity and linking,
// topics), a runner that analyses the gold set with any configured provider
// through the production prompt and validation code, reports, calibration
// and the cost model.
//
// The gold set holds real personal data: it lives on the owner's server,
// never in the repository (testdata only holds synthetic examples).
package eval

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
)

// Item is one gold-set entry: the text to label (with its context) and,
// once labelled, its label.
type Item struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // post | comment | reply | image
	Page   string `json:"page,omitempty"`
	Post   string `json:"post,omitempty"`
	Parent string `json:"parent,omitempty"`
	Text   string `json:"text,omitempty"`
	// ImagePath is a local file (images only), on the owner's server.
	ImagePath string `json:"image_path,omitempty"`
	// Strata explains why the item was sampled (page, length, script...).
	Strata []string `json:"strata,omitempty"`
	Label  *Label   `json:"label,omitempty"`
}

// Label is the native speaker's annotation (docs/analysis/labelling-guide.md).
type Label struct {
	Lang      string   `json:"lang"`
	Dialect   string   `json:"dialect"`
	Sentiment string   `json:"sentiment"`
	Sarcasm   bool     `json:"sarcasm"`
	Entities  []Entity `json:"entities"`
	Topics    []string `json:"topics"`
	Intent    string   `json:"intent"`
	Emotion   string   `json:"emotion"`
	Toxicity  string   `json:"toxicity"`
	Spam      bool     `json:"spam"`
	Urgent    bool     `json:"urgent"`
	// Ad: own | paid | hidden | sale, "" when not an ad. Legal: possible
	// breaches (prompt.LegalFlags).
	Ad    string   `json:"ad,omitempty"`
	Legal []string `json:"legal,omitempty"`
	// Images: logos seen (registry ids or names) and words that must appear
	// in the text read from the image.
	Logos       []string `json:"logos,omitempty"`
	OCRContains []string `json:"ocr_contains,omitempty"`
	// Unsure items are excluded from the metrics (and reviewed).
	Unsure     bool      `json:"unsure"`
	Notes      string    `json:"notes,omitempty"`
	Labeller   string    `json:"labeller,omitempty"`
	LabelledAt time.Time `json:"labelled_at,omitempty"`
}

// Entity is a labelled entity mention with the sentiment toward it.
type Entity struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	Sentiment string `json:"sentiment"`
	Aspect    string `json:"aspect,omitempty"`
}

// GoldID derives an opaque item id from the source content id, so gold
// files do not carry platform ids.
func GoldID(contentID string) string {
	h := sha256.Sum256([]byte("gold:" + contentID))
	return "g-" + hex.EncodeToString(h[:8])
}

// ReadItems reads a JSONL file of items.
func ReadItems(path string) ([]Item, error) {
	f, err := os.Open(path) // #nosec G304 -- operator-named file
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Item
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	line := 0
	for sc.Scan() {
		line++
		if len(sc.Bytes()) == 0 {
			continue
		}
		var it Item
		if err := json.Unmarshal(sc.Bytes(), &it); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		out = append(out, it)
	}
	return out, sc.Err()
}

// WriteItems writes items as JSONL atomically (temp file, then rename),
// readable by the owner only.
func WriteItems(path string, items []Item) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*.jsonl")
	if err != nil {
		return err
	}
	w := bufio.NewWriter(tmp)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for i := range items {
		if err := enc.Encode(&items[i]); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
			return err
		}
	}
	if err := w.Flush(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Labelled returns the items with a usable (not unsure) label, without the
// repeated copies.
func Labelled(items []Item) []Item {
	var out []Item
	for _, it := range items {
		if strings.HasSuffix(it.ID, RepeatSuffix) {
			continue
		}
		if it.Label != nil && !it.Label.Unsure && it.Label.Sentiment != "" {
			out = append(out, it)
		}
	}
	return out
}

// RepeatSuffix marks a copy of an item labelled a second time, to measure
// the labeller's consistency; copies are left out of model scoring.
const RepeatSuffix = "-r"

// AddRepeats appends copies of every n-th item (share 0..1) with
// RepeatSuffix ids, for a second blind labelling later in the session.
func AddRepeats(items []Item, share float64) []Item {
	if share <= 0 {
		return items
	}
	step := int(1 / share)
	if step < 1 {
		step = 1
	}
	out := append([]Item(nil), items...)
	for i := 0; i < len(items); i += step {
		c := items[i]
		c.ID += RepeatSuffix
		c.Label = nil
		out = append(out, c)
	}
	return out
}

// Agreement is the labeller's consistency on repeated items.
type Agreement struct {
	Pairs          int
	SentimentKappa float64
	SentimentAgree float64
	EntityF1       float64
	TopicF1        float64
}

// SelfAgreement compares each repeated item with its original.
func SelfAgreement(items []Item) Agreement {
	byID := map[string]Item{}
	for _, it := range items {
		byID[it.ID] = it
	}
	var a, b []string
	var la []Label
	var lb []Label
	var ta, tb [][]string
	for _, it := range items {
		if len(it.ID) <= len(RepeatSuffix) || it.ID[len(it.ID)-len(RepeatSuffix):] != RepeatSuffix || it.Label == nil || it.Label.Unsure {
			continue
		}
		orig, ok := byID[it.ID[:len(it.ID)-len(RepeatSuffix)]]
		if !ok || orig.Label == nil || orig.Label.Unsure {
			continue
		}
		a, b = append(a, orig.Label.Sentiment), append(b, it.Label.Sentiment)
		la, lb = append(la, *orig.Label), append(lb, *it.Label)
		ta, tb = append(ta, orig.Label.Topics), append(tb, it.Label.Topics)
	}
	ag := Agreement{Pairs: len(a)}
	if len(a) == 0 {
		return ag
	}
	ag.SentimentAgree = Accuracy(a, b)
	ag.SentimentKappa = Kappa(a, b)
	// Score the second labelling's entities as "predictions" of the first.
	preds := make([]core.Result, len(lb))
	for i, l := range lb {
		for _, e := range l.Entities {
			preds[i].Entities = append(preds[i].Entities, core.Entity{ID: e.ID, Name: e.Name, Type: e.Type, Sentiment: e.Sentiment})
		}
	}
	ag.EntityF1 = ScoreEntities(la, preds).Entities.F1
	ag.TopicF1 = ScoreTopics(ta, tb, 1).Micro.F1
	return ag
}

// Kappa is Cohen's kappa of two labelings.
func Kappa(a, b []string) float64 {
	n := float64(len(a))
	if n == 0 {
		return 0
	}
	ca, cb := map[string]float64{}, map[string]float64{}
	agree := 0.0
	for i := range a {
		ca[a[i]]++
		cb[b[i]]++
		if a[i] == b[i] {
			agree++
		}
	}
	po := agree / n
	pe := 0.0
	for k, v := range ca {
		pe += (v / n) * (cb[k] / n)
	}
	if pe == 1 {
		return 1
	}
	return (po - pe) / (1 - pe)
}
