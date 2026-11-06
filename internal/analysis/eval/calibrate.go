package eval

import (
	"sort"
)

// Calibrate fits an isotonic (monotone non-decreasing) map from a model's
// raw sentiment confidence to the observed share of correct labels, with
// pool-adjacent-violators, and returns it as (raw, calibrated) points
// (the format of pipeline.Calibration).
func Calibrate(gold []Item, reqs []Request) [][2]float64 {
	byID := map[string]Item{}
	for _, it := range Labelled(gold) {
		byID[it.ID] = it
	}
	type obs struct{ x, y float64 }
	var xs []obs
	for _, r := range reqs {
		for _, p := range r.Items {
			g, ok := byID[p.ID]
			if !ok || p.Result == nil {
				continue
			}
			y := 0.0
			if p.Result.Sentiment == g.Label.Sentiment {
				y = 1
			}
			xs = append(xs, obs{p.Result.Confidence, y})
		}
	}
	if len(xs) == 0 {
		return nil
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i].x < xs[j].x })
	type block struct {
		xLo, xHi, sum float64
		n             int
	}
	var bs []block
	for _, o := range xs {
		bs = append(bs, block{o.x, o.x, o.y, 1})
		for len(bs) > 1 {
			a, b := bs[len(bs)-2], bs[len(bs)-1]
			if a.sum/float64(a.n) <= b.sum/float64(b.n) {
				break
			}
			bs = append(bs[:len(bs)-2], block{a.xLo, b.xHi, a.sum + b.sum, a.n + b.n})
		}
	}
	var pts [][2]float64
	for _, b := range bs {
		v := b.sum / float64(b.n)
		pts = append(pts, [2]float64{b.xLo, v})
		if b.xHi != b.xLo {
			pts = append(pts, [2]float64{b.xHi, v})
		}
	}
	return pts
}
