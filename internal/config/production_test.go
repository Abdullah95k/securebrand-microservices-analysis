package config

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

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
// (the contract's topics and fbs's own), a $10 daily hard stop, Claude
// Haiku for every AI call, and speech transcribed inside the cluster.
func TestProductionConfig(t *testing.T) {
	c, err := Load(chartConfig(t, "values.yaml") + "," + chartConfig(t, "values-production.yaml"))
	if err != nil {
		t.Fatalf("production config refused: %v", err)
	}
	a := c.Analysis
	if !slices.Equal(a.Inputs, []string{"content", "fb"}) {
		t.Errorf("inputs %v, want [content fb]", a.Inputs)
	}
	if a.Budget.DailyUSD != 10 || a.Budget.AllowUnlimited || a.Budget.UrgentExempt {
		t.Errorf("budget %+v, want $10 a day, a hard stop for urgent items too", a.Budget)
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
	// Speech in videos (the owner, 7 November 2026): the whisper server in
	// the cluster, one video at a time, the first 10 minutes at most, with
	// a timeout and a lease that fit a slow CPU transcription.
	v := a.Media.Video
	if v.STTProvider != "whisper" || v.STTConcurrency != 1 || v.MaxSeconds > 600 {
		t.Errorf("speech-to-text %q, %d at once, %ds: want whisper, 1, at most 600s", v.STTProvider, v.STTConcurrency, v.MaxSeconds)
	}
	if w := a.Providers["whisper"]; w.Timeout < 15*time.Minute || w.MaxRetries != 0 {
		t.Errorf("whisper timeout %v, retries %d: want at least 15m and no immediate retry", w.Timeout, w.MaxRetries)
	}
	if a.Media.Lease < 30*time.Minute {
		t.Errorf("media lease %v is shorter than a slow transcription", a.Media.Lease)
	}
}
