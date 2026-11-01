package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

// Calibration maps a model's raw confidence to observed accuracy, fitted on
// the gold set by sb-eval (isotonic, stored as points). Without a map the
// raw value is used.
type Calibration struct {
	Model string `yaml:"model"`
	// Points are (raw, calibrated) pairs, raw ascending.
	Points [][2]float64 `yaml:"points"`
}

// Apply interpolates linearly between points.
func (c *Calibration) Apply(raw float64) float64 {
	if c == nil || len(c.Points) == 0 {
		return raw
	}
	p := c.Points
	if raw <= p[0][0] {
		return p[0][1]
	}
	for i := 1; i < len(p); i++ {
		if raw <= p[i][0] {
			x0, y0, x1, y1 := p[i-1][0], p[i-1][1], p[i][0], p[i][1]
			if x1 == x0 {
				return y1
			}
			return y0 + (raw-x0)*(y1-y0)/(x1-x0)
		}
	}
	return p[len(p)-1][1]
}

// LoadCalibrations reads every <model>.yaml in dir ("" = none).
func LoadCalibrations(dir string) (map[string]*Calibration, error) {
	out := map[string]*Calibration{}
	if dir == "" {
		return out, nil
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		b, err := os.ReadFile(f) // #nosec G304 -- operator-configured directory
		if err != nil {
			return nil, err
		}
		var c Calibration
		if err := yaml.Unmarshal(b, &c); err != nil {
			return nil, fmt.Errorf("calibration %s: %w", f, err)
		}
		if c.Model == "" {
			return nil, fmt.Errorf("calibration %s: model is required", f)
		}
		sort.Slice(c.Points, func(i, j int) bool { return c.Points[i][0] < c.Points[j][0] })
		out[c.Model] = &c
	}
	return out, nil
}
