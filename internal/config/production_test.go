package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

// chartConfig writes the "config" section of a chart values file to a file
// of its own, as the chart's ConfigMap renders it.
func chartConfig(t *testing.T, values string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "deploy", "helm", "sb-analysis", values))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Config map[string]any `yaml:"config"`
	}
	if err := yaml.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	out, err := yaml.Marshal(v.Config)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), values)
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestProductionConfig loads production's configuration as the chart
// builds it (the defaults, the chart's values, then values-production) and
// checks the owner's decisions of 7 November 2026: every collector read
// (the contract's topics and fbs's own), no daily cap, Claude Haiku only.
func TestProductionConfig(t *testing.T) {
	c, err := Load(chartConfig(t, "values.yaml") + "," + chartConfig(t, "values-production.yaml"))
	if err != nil {
		t.Fatalf("production config refused: %v", err)
	}
	a := c.Analysis
	if !slices.Equal(a.Inputs, []string{"content", "fb"}) {
		t.Errorf("inputs %v, want [content fb]", a.Inputs)
	}
	if a.Budget.DailyUSD != 0 || !a.Budget.AllowUnlimited {
		t.Errorf("budget %+v, want no cap", a.Budget)
	}
	const haiku = "claude-haiku-5-5"
	for name, r := range a.Routes {
		if r.On() && r.Model != haiku {
			t.Errorf("route %s runs %s, want %s only", name, r.Model, haiku)
		}
	}
	if a.Topics.Curation.Model != haiku {
		t.Errorf("curation runs %s, want %s", a.Topics.Curation.Model, haiku)
	}
	if a.Media.Video.STTProvider != "" {
		t.Errorf("speech-to-text %q is on: transcripts come from captions only", a.Media.Video.STTProvider)
	}
}
