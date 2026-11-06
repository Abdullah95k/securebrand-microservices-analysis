package taxonomy

import (
	"strings"
	"testing"
)

func TestDefaultTaxonomy(t *testing.T) {
	tx, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if tx.Version != "tax-v1" || tx.Len() < 100 || !tx.Valid("telecom.network") || !tx.Valid("other") || tx.Valid("telecom") == false {
		t.Fatalf("taxonomy %s with %d topics", tx.Version, tx.Len())
	}
	if tx.Valid("telecom.nope") {
		t.Fatal("unknown topic valid")
	}
	if !strings.Contains(tx.Prompt(), "  telecom.network: Network coverage and call quality / ") {
		t.Fatal("prompt rendering")
	}
	if len(tx.IDs()) != tx.Len() {
		t.Fatal("ids")
	}
}

func TestParseRejects(t *testing.T) {
	for name, doc := range map[string]string{
		"no version":   "topics: [{id: a}]",
		"no topics":    "version: v",
		"duplicate":    "version: v\ntopics: [{id: a}, {id: a}]",
		"bad child":    "version: v\ntopics: [{id: a, children: [{id: b.c}]}]",
		"empty id":     "version: v\ntopics: [{id: ''}]",
		"invalid yaml": "version: [",
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Load("/nonexistent.yaml"); err == nil {
		t.Fatal("missing file accepted")
	}
}
