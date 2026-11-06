// Package bots scores how likely an author is a bot or a fake account
// from behaviour alone: the analysis service has no profile data (author
// ids are pseudonymous), so it looks at volume, bursts, repetition across
// threads, the same text from many authors (coordination), replies within
// seconds of a post, round-the-clock activity and the share of spam, ads
// and scams. Each rule adds points; the reasons are kept so a person can
// check them. Paid human "electronic armies" behave the same way and are
// flagged the same way. The thresholds are starting values to tune on the
// pilot data (docs/design/analysis-service.md section 6.2).
package bots

import "github.com/abdullah95k/securebrand-microservices-analysis/internal/core"

// Levels.
const (
	Suspected = "suspected"
	Likely    = "likely"
)

// Thresholds turn a score into a level.
type Thresholds struct {
	Suspected int
	Likely    int
}

// rule adds points when a signal crosses a value; the strong value adds
// more.
type rule struct {
	reason         string
	value          func(core.AuthorSignals) float64
	weak, strong   float64
	weakP, strongP int
	// applies: the rule needs enough data to be meaningful.
	applies func(core.AuthorSignals) bool
}

// rules: coordination weighs most (the same long text from several
// accounts is the clearest sign of a network), then bursts and copy-paste
// across threads, then spam share and volume; breadth, speed and
// round-the-clock activity only add to other evidence.
var rules = []rule{
	{reason: "burst", value: func(s core.AuthorSignals) float64 { return float64(s.MaxPerHour) }, weak: 15, strong: 30, weakP: 10, strongP: 25},
	{reason: "volume", value: func(s core.AuthorSignals) float64 { return s.PerDay() }, weak: 20, strong: 50, weakP: 10, strongP: 20},
	{reason: "repetition", value: func(s core.AuthorSignals) float64 { return s.Repeated }, weak: 0.25, strong: 0.5, weakP: 10, strongP: 25},
	{reason: "coordinated", value: func(s core.AuthorSignals) float64 { return s.Coordinated }, weak: 0.1, strong: 0.3, weakP: 15, strongP: 35},
	// A network account: two or more coordinated posts, however few posts
	// it has in all (with a strong coordination share: suspected).
	{reason: "network", value: func(s core.AuthorSignals) float64 { return float64(s.CoordinatedItems) }, weak: 2, strong: 2, weakP: 15, strongP: 15},
	{reason: "fast_replies", value: func(s core.AuthorSignals) float64 { return s.Fast }, weak: 0.3, strong: 0.5, weakP: 5, strongP: 15,
		applies: func(s core.AuthorSignals) bool { return s.FastBase >= 10 }},
	{reason: "spam", value: func(s core.AuthorSignals) float64 { return s.Spammy }, weak: 0.25, strong: 0.5, weakP: 10, strongP: 20},
	{reason: "breadth", value: func(s core.AuthorSignals) float64 { return float64(s.Sources) / float64(max(s.ActiveDays, 1)) },
		weak: 10, strong: 20, weakP: 5, strongP: 10},
	{reason: "no_sleep", value: func(s core.AuthorSignals) float64 { return float64(s.ActiveHours) }, weak: 20, strong: 23, weakP: 5, strongP: 10,
		applies: func(s core.AuthorSignals) bool { return s.PerDay() >= 10 }},
}

// Score returns the author's score (0-100), level ("" when not flagged)
// and the reasons behind it.
func Score(s core.AuthorSignals, t Thresholds) (int, string, []string) {
	score := 0
	var reasons []string
	for _, r := range rules {
		if r.applies != nil && !r.applies(s) {
			continue
		}
		v := r.value(s)
		switch {
		case v >= r.strong:
			score += r.strongP
		case v >= r.weak:
			score += r.weakP
		default:
			continue
		}
		reasons = append(reasons, r.reason)
	}
	score = min(score, 100)
	level := ""
	switch {
	case t.Likely > 0 && score >= t.Likely:
		level = Likely
	case t.Suspected > 0 && score >= t.Suspected:
		level = Suspected
	}
	return score, level, reasons
}
