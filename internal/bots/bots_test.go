package bots

import (
	"strings"
	"testing"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
)

var th = Thresholds{Suspected: 45, Likely: 70}

func TestScore(t *testing.T) {
	cases := []struct {
		name    string
		s       core.AuthorSignals
		level   string
		reasons string
	}{
		{"a normal person", core.AuthorSignals{Items: 30, ActiveDays: 10, MaxPerHour: 3, Sources: 4, ActiveHours: 9}, "", ""},
		{"a copy-paste bot", core.AuthorSignals{Items: 300, ActiveDays: 3, MaxPerHour: 40, Sources: 30, ActiveHours: 12, Repeated: 0.8, Spammy: 0.6},
			Likely, "burst,volume,repetition,spam,breadth"},
		{"part of a network", core.AuthorSignals{Items: 40, ActiveDays: 4, MaxPerHour: 5, Sources: 8, ActiveHours: 10, Coordinated: 0.4, Repeated: 0.3},
			Suspected, "repetition,coordinated"},
		// A small network account: two posts, both copies of a campaign text.
		{"network account", core.AuthorSignals{Items: 2, ActiveDays: 1, MaxPerHour: 1, Sources: 2, ActiveHours: 1, Coordinated: 1, CoordinatedItems: 2},
			Suspected, "coordinated,network"},
		{"one shared post", core.AuthorSignals{Items: 6, ActiveDays: 3, Coordinated: 1.0 / 6, CoordinatedItems: 1}, "", "coordinated"},
		// Fast replies need enough comments to mean something.
		{"few fast replies", core.AuthorSignals{Items: 12, ActiveDays: 6, Fast: 1, FastBase: 5}, "", ""},
		{"first-comment bot", core.AuthorSignals{Items: 60, ActiveDays: 3, MaxPerHour: 16, Fast: 0.9, FastBase: 60, Spammy: 0.3, Repeated: 0.3},
			Suspected, "burst,volume,repetition,fast_replies,spam"},
		// Round the clock counts only with real volume.
		{"night owl", core.AuthorSignals{Items: 24, ActiveDays: 4, ActiveHours: 23}, "", ""},
		{"never sleeps", core.AuthorSignals{Items: 400, ActiveDays: 8, ActiveHours: 24, MaxPerHour: 10}, "", "volume,no_sleep"},
	}
	for _, c := range cases {
		score, level, reasons := Score(c.s, th)
		if level != c.level || strings.Join(reasons, ",") != c.reasons {
			t.Errorf("%s: score %d level %q reasons %v, want %q %q", c.name, score, level, reasons, c.level, c.reasons)
		}
	}
	// The score is capped, and no thresholds means no levels.
	all := core.AuthorSignals{Items: 1000, ActiveDays: 1, MaxPerHour: 100, Sources: 100, ActiveHours: 24, Repeated: 1, Coordinated: 1,
		Fast: 1, FastBase: 100, Spammy: 1}
	if score, level, _ := Score(all, th); score != 100 || level != Likely {
		t.Fatalf("everything: %d %q", score, level)
	}
	if _, level, _ := Score(all, Thresholds{}); level != "" {
		t.Fatal("level without thresholds")
	}
}
