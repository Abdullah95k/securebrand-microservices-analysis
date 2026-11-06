package eval

import (
	"strings"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
)

// SilverLabeller marks labels written by a reference model, not a person.
const SilverLabeller = "model:"

// LabelFromResult turns a reference model's analysis into a label
// ("silver" labels: the owner chose no human labeller). Entity topics
// ("@...") are not labels; private persons were never entities.
func LabelFromResult(r *core.Result, model string) *Label {
	l := &Label{Lang: r.Lang, Dialect: r.Dialect, Sentiment: r.Sentiment, Sarcasm: r.Sarcasm, Intent: r.Intent, Emotion: r.Emotion,
		Toxicity: r.Toxicity, Spam: r.Spam, Urgent: r.Urgent, Ad: r.AdType, Legal: append([]string(nil), r.Legal...),
		Labeller: SilverLabeller + model, LabelledAt: time.Now().UTC()}
	for _, t := range r.Topics {
		if !strings.HasPrefix(t, "@") {
			l.Topics = append(l.Topics, t)
		}
	}
	for _, e := range r.Entities {
		l.Entities = append(l.Entities, Entity{Name: e.Name, Type: e.Type, ID: e.ID, Sentiment: e.Sentiment, Aspect: e.Aspect})
	}
	return l
}

// Silver fills the labels of the unlabelled text items from a reference
// model's predictions in reqs (one run). Items the model missed, refused
// or failed stay unlabelled. It returns how many items it labelled.
func Silver(items []Item, reqs []Request, model string) int {
	pred := map[string]*core.Result{}
	for _, r := range reqs {
		if r.Model != model || r.Refused || r.Error != "" {
			continue
		}
		for _, p := range r.Items {
			if p.Result != nil && !p.Result.Refused {
				pred[p.ID] = p.Result
			}
		}
	}
	n := 0
	for i := range items {
		if items[i].Label != nil || items[i].Kind == "image" {
			continue
		}
		if r, ok := pred[items[i].ID]; ok {
			items[i].Label = LabelFromResult(r, model)
			n++
		}
	}
	return n
}

// Unlabelled returns the text items without a label.
func Unlabelled(items []Item) []Item {
	var out []Item
	for _, it := range items {
		if it.Label == nil && it.Kind != "image" {
			out = append(out, it)
		}
	}
	return out
}
