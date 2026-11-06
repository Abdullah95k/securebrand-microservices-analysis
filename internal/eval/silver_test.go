package eval

import (
	"strings"
	"testing"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
)

func TestSilverLabels(t *testing.T) {
	items := []Item{
		{ID: "a", Kind: "comment", Text: "x"},
		{ID: "b", Kind: "comment", Text: "y", Label: &Label{Sentiment: "positive", Labeller: "L1"}}, // labelled: kept
		{ID: "c", Kind: "image", ImagePath: "/gold/1.jpg"},                                          // images: not by text
		{ID: "d", Kind: "comment", Text: "z"},                                                       // refused: stays unlabelled
		{ID: "e", Kind: "post", Text: "w"},                                                          // another model: ignored
	}
	if n := len(Unlabelled(items)); n != 3 {
		t.Fatalf("unlabelled %d, want 3", n)
	}
	res := core.Result{Lang: "ar", Dialect: "iraqi", Sentiment: "negative", Topics: []string{"telecom.network", "@asiacell"}, Intent: "complaint",
		Emotion: "anger", Toxicity: "none", AdType: "", Legal: []string{"defamation"},
		Entities: []core.Entity{{ID: "asiacell", Name: "Asiacell", Type: "brand", Sentiment: "negative", Aspect: "network"}}}
	reqs := []Request{
		{Model: "claude-opus-5-5", Items: []Prediction{{ID: "a", Result: &res}, {ID: "b", Result: &res}, {ID: "d", Result: &core.Result{Refused: true}}}},
		{Model: "claude-haiku-5-5", Items: []Prediction{{ID: "e", Result: &res}}},
	}
	if n := Silver(items, reqs, "claude-opus-5-5"); n != 1 {
		t.Fatalf("labelled %d, want 1", n)
	}
	l := items[0].Label
	if l == nil || l.Sentiment != "negative" || strings.Join(l.Topics, ",") != "telecom.network" || len(l.Entities) != 1 ||
		l.Entities[0].ID != "asiacell" || l.Labeller != "model:claude-opus-5-5" || strings.Join(l.Legal, ",") != "defamation" {
		t.Fatalf("silver label %+v", l)
	}
	if items[1].Label.Labeller != "L1" || items[3].Label != nil || items[4].Label != nil {
		t.Fatal("silver touched an item it should not")
	}
}
