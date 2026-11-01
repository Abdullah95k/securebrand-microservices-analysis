package eval

import (
	"math"
	"math/rand/v2"
	"sort"
	"strings"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/textnorm"
)

// PRF is precision, recall and F1.
type PRF struct {
	P, R, F1 float64
	TP, FP   int
	FN       int
}

func prf(tp, fp, fn int) PRF {
	p := PRF{TP: tp, FP: fp, FN: fn}
	if tp+fp > 0 {
		p.P = float64(tp) / float64(tp+fp)
	}
	if tp+fn > 0 {
		p.R = float64(tp) / float64(tp+fn)
	}
	if p.P+p.R > 0 {
		p.F1 = 2 * p.P * p.R / (p.P + p.R)
	}
	return p
}

// MacroF1 is the unweighted mean F1 over labels that occur in gold or
// predictions.
func MacroF1(gold, pred []string) float64 {
	labels := map[string]bool{}
	for i := range gold {
		labels[gold[i]] = true
		labels[pred[i]] = true
	}
	if len(labels) == 0 {
		return 0
	}
	sum := 0.0
	for l := range labels {
		tp, fp, fn := 0, 0, 0
		for i := range gold {
			switch {
			case gold[i] == l && pred[i] == l:
				tp++
			case pred[i] == l:
				fp++
			case gold[i] == l:
				fn++
			}
		}
		sum += prf(tp, fp, fn).F1
	}
	return sum / float64(len(labels))
}

// Accuracy is the share of equal labels.
func Accuracy(gold, pred []string) float64 {
	if len(gold) == 0 {
		return 0
	}
	n := 0
	for i := range gold {
		if gold[i] == pred[i] {
			n++
		}
	}
	return float64(n) / float64(len(gold))
}

// BinaryF1 is the F1 of the positive class.
func BinaryF1(gold, pred []bool) PRF {
	tp, fp, fn := 0, 0, 0
	for i := range gold {
		switch {
		case gold[i] && pred[i]:
			tp++
		case pred[i]:
			fp++
		case gold[i]:
			fn++
		}
	}
	return prf(tp, fp, fn)
}

// Bootstrap returns the 95% interval of metric over n resamples (fixed
// seed, so reports are reproducible).
func Bootstrap(gold, pred []string, metric func(g, p []string) float64, n int) (lo, hi float64) {
	if len(gold) == 0 || n <= 0 {
		return 0, 0
	}
	r := rand.New(rand.NewPCG(42, 7))
	vals := make([]float64, n)
	g, p := make([]string, len(gold)), make([]string, len(gold))
	for k := 0; k < n; k++ {
		for i := range gold {
			j := r.IntN(len(gold))
			g[i], p[i] = gold[j], pred[j]
		}
		vals[k] = metric(g, p)
	}
	sort.Float64s(vals)
	return vals[int(0.025*float64(n))], vals[int(math.Min(float64(n-1), 0.975*float64(n)))]
}

// Collapse3 maps the four sentiment labels to three (mixed -> neutral).
func Collapse3(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		if x == core.SentimentMixed {
			x = core.SentimentNeutral
		}
		out[i] = x
	}
	return out
}

// entityKey identifies an entity for matching: its registry id when known,
// else its type and normalised name.
func entityKey(id, typ, name string) string {
	if id != "" {
		return "id:" + id
	}
	return typ + ":" + textnorm.Normalize(name)
}

// EntityScores compares gold and predicted entities per item.
type EntityScores struct {
	// Entities: a predicted entity matches a gold one by registry id, or by
	// type and normalised name when either has no id.
	Entities PRF
	// Aspect: (entity, sentiment) pairs.
	Aspect PRF
	// AspectAccuracy: sentiment correct among matched entities.
	AspectAccuracy float64
	// Linking: share of gold entities with a registry id whose id the
	// prediction found.
	Linking float64
	// TrackedAspect: Aspect restricted to gold entities with a registry id
	// (the brands clients track).
	TrackedAspect PRF
}

// ScoreEntities computes EntityScores over aligned gold labels and results.
func ScoreEntities(gold []Label, pred []core.Result) EntityScores {
	var e, a, ta [3]int
	matched, correct := 0, 0
	linkWant, linkGot := 0, 0
	for i := range gold {
		g := map[string]Entity{}
		names := map[string]string{} // type:name -> key, to match an unlinked prediction to a linked gold entity
		for _, x := range gold[i].Entities {
			if x.Type == "person_private" {
				continue
			}
			k := entityKey(x.ID, x.Type, x.Name)
			g[k] = x
			names[x.Type+":"+textnorm.Normalize(x.Name)] = k
			if x.ID != "" {
				linkWant++
			}
		}
		p := map[string]core.Entity{}
		for _, x := range pred[i].Entities {
			k := entityKey(x.ID, x.Type, x.Name)
			if _, ok := g[k]; !ok {
				if alt, ok := names[x.Type+":"+textnorm.Normalize(x.Name)]; ok {
					k = alt
				}
			}
			p[k] = x
		}
		for k, gx := range g {
			px, ok := p[k]
			if !ok {
				e[2]++
				a[2]++
				if gx.ID != "" {
					ta[2]++
				}
				continue
			}
			e[0]++
			matched++
			if gx.ID != "" && px.ID == gx.ID {
				linkGot++
			}
			if px.Sentiment == gx.Sentiment {
				correct++
				a[0]++
				if gx.ID != "" {
					ta[0]++
				}
			} else {
				a[1]++
				a[2]++
				if gx.ID != "" {
					ta[1]++
					ta[2]++
				}
			}
		}
		for k, px := range p {
			if _, ok := g[k]; !ok {
				e[1]++
				a[1]++
				if px.ID != "" {
					ta[1]++
				}
			}
		}
	}
	s := EntityScores{Entities: prf(e[0], e[1], e[2]), Aspect: prf(a[0], a[1], a[2]), TrackedAspect: prf(ta[0], ta[1], ta[2])}
	if matched > 0 {
		s.AspectAccuracy = float64(correct) / float64(matched)
	}
	if linkWant > 0 {
		s.Linking = float64(linkGot) / float64(linkWant)
	}
	return s
}

// TopicScores are multi-label topic scores.
type TopicScores struct {
	Micro PRF
	Macro float64
	// TopLevel: scores after mapping topics to their top-level parent.
	TopLevel PRF
}

// ScoreTopics compares topic sets; Macro averages over topics with at
// least minSupport gold occurrences.
func ScoreTopics(gold, pred [][]string, minSupport int) TopicScores {
	micro := func(g, p [][]string) PRF {
		tp, fp, fn := 0, 0, 0
		for i := range g {
			gs := set(g[i])
			ps := set(p[i])
			for t := range ps {
				if gs[t] {
					tp++
				} else {
					fp++
				}
			}
			for t := range gs {
				if !ps[t] {
					fn++
				}
			}
		}
		return prf(tp, fp, fn)
	}
	s := TopicScores{Micro: micro(gold, pred)}
	support := map[string]int{}
	for _, g := range gold {
		for _, t := range g {
			support[t]++
		}
	}
	n, sum := 0, 0.0
	for t, c := range support {
		if c < minSupport {
			continue
		}
		tp, fp, fn := 0, 0, 0
		for i := range gold {
			gi, pi := set(gold[i])[t], set(pred[i])[t]
			switch {
			case gi && pi:
				tp++
			case pi:
				fp++
			case gi:
				fn++
			}
		}
		sum += prf(tp, fp, fn).F1
		n++
	}
	if n > 0 {
		s.Macro = sum / float64(n)
	}
	top := func(xs [][]string) [][]string {
		out := make([][]string, len(xs))
		for i, x := range xs {
			for _, t := range x {
				out[i] = append(out[i], strings.SplitN(t, ".", 2)[0])
			}
		}
		return out
	}
	s.TopLevel = micro(top(gold), top(pred))
	return s
}

func set(xs []string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}
