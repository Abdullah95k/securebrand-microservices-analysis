package main

import (
	"testing"

	"github.com/abdullah95k/securebrand-microservices-analysis/configs"
)

// Next to a running fbs the analysis creates only its own topics: an fbs
// topic in the list would have its retention reset to this copy's value.
func TestOwnTopicsOnly(t *testing.T) {
	b, err := configs.FS.ReadFile("topics.yaml")
	if err != nil {
		t.Fatal(err)
	}
	specs, replicas, err := ownTopics(b, 3)
	if err != nil {
		t.Fatal(err)
	}
	if replicas <= 0 || len(specs) == 0 {
		t.Fatalf("replicas %d, %d topics", replicas, len(specs))
	}
	seen := map[string]bool{}
	for _, s := range specs {
		seen[s.Name] = true
		if s.Partitions > 3 {
			t.Errorf("%s: %d partitions over the cap", s.Name, s.Partitions)
		}
		if s.Name[:3] == "fb." || s.Name[:4] == "fbs." {
			t.Errorf("fbs topic %s in the analysis list", s.Name)
		}
	}
	for _, want := range []string{"analysis.result.v1", "analysis.author.v1", "content.item.v1"} {
		if !seen[want] {
			t.Errorf("%s missing", want)
		}
	}
}
