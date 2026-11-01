package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/textnorm"
)

// ReadRequests reads a results file.
func ReadRequests(path string) ([]Request, error) {
	f, err := os.Open(path) // #nosec G304 -- operator-named file
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	out, err := DecodeRequests(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return out, nil
}

// DecodeRequests reads results records, one JSON object per line.
func DecodeRequests(rd io.Reader) ([]Request, error) {
	var out []Request
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var r Request
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// Scores summarises one configuration.
type Scores struct {
	Config      string
	Items       int // labelled text items with a prediction slot
	Coverage    float64
	RefusalRate float64 // requests
	ErrorRate   float64 // requests
	SentF1      float64 // 4 classes, macro
	SentCI      [2]float64
	Sent3F1     float64
	Entities    EntityScores
	Topics      TopicScores
	IntentF1    float64
	EmotionAcc  float64
	LangAcc     float64
	DialectAcc  float64
	Toxic       PRF
	Sarcasm     PRF
	Spam        PRF
	Urgent      PRF
	// Ad: is an ad (binary); AdTypeAcc: the type, on items both call ads.
	Ad        PRF
	AdTypeAcc float64
	// Legal: any possible breach (binary); LegalCat: categories, micro.
	Legal     PRF
	LegalCat  PRF
	Images    int
	LogoF1    PRF
	OCRRecall float64
	// Measured cost and tokens per text item, and latency per request.
	USDPer1K        float64
	TokensIn        float64
	TokensCached    float64
	TokensOut       float64
	LatencyP50MS    int64
	LatencyP95MS    int64
	ImageUSDPer1K   float64
	CostPerComment  float64
	CommentsCounted int
}

// Score joins results with the gold set, one Scores per configuration
// (provider, model, lane, group size, thinking, effort).
func Score(gold []Item, reqs []Request) []Scores {
	byID := map[string]Item{}
	for _, it := range Labelled(gold) {
		byID[it.ID] = it
	}
	cfgs := map[string][]Request{}
	for _, r := range reqs {
		k := fmt.Sprintf("%s/%s thinking=%s effort=%s", r.Provider, r.Model, r.Thinking, r.Effort)
		if r.GroupSize > 1 || r.Lane == "batch" {
			k += fmt.Sprintf(" %s g=%d", r.Lane, r.GroupSize)
		}
		cfgs[k] = append(cfgs[k], r)
	}
	var out []Scores
	for _, k := range sortedKeys(cfgs) {
		out = append(out, scoreConfig(k, cfgs[k], byID))
	}
	return out
}

func scoreConfig(name string, reqs []Request, gold map[string]Item) Scores {
	s := Scores{Config: name}
	var (
		gs, ps                     []string
		gl                         []Label
		pr                         []core.Result
		gt, pt                     [][]string
		gi, pi, ge, pe, gla, pla   []string
		gd, pd                     []string
		tox, toxP, sar, sarP       []bool
		spam, spamP, urg, urgP     []bool
		ad, adP, leg, legP         []bool
		adT, adTP                  []string
		legCat                     [3]int
		lat                        []int64
		textCost, imgCost          float64
		textItems, imgItems        int
		inTok, cacheTok, outTok    int
		refused, errored, predSlot int
		covered                    int
		logo                       [3]int
		ocrWant, ocrGot            int
		commentCost                float64
		comments                   int
	)
	for _, r := range reqs {
		lat = append(lat, r.LatencyMS)
		if r.Refused {
			refused++
		}
		if r.Error != "" {
			errored++
		}
		isImage := len(r.Items) == 1 && (r.Items[0].Media != nil || (gold[r.Items[0].ID].Kind == "image"))
		if isImage {
			imgCost += r.CostUSD
			imgItems++
		} else {
			textCost += r.CostUSD
			textItems += len(r.Items)
			inTok += r.Usage.InputTokens
			cacheTok += r.Usage.CacheReadTokens
			outTok += r.Usage.OutputTokens
			allComments := true
			for _, p := range r.Items {
				if g, ok := gold[p.ID]; ok && g.Kind == core.KindPost {
					allComments = false
				}
			}
			if allComments {
				commentCost += r.CostUSD
				comments += len(r.Items)
			}
		}
		for _, p := range r.Items {
			g, ok := gold[p.ID]
			if !ok {
				continue
			}
			if g.Kind == "image" {
				s.Images++
				scoreImage(g.Label, p.Media, &logo, &ocrWant, &ocrGot)
				continue
			}
			predSlot++
			res := core.Result{Sentiment: core.SentimentNeutral, Lang: "missing", Dialect: "missing", Intent: "missing", Emotion: "missing", Toxicity: "none"}
			if p.Result != nil {
				res = *p.Result
				covered++
			}
			l := *g.Label
			gs, ps = append(gs, l.Sentiment), append(ps, res.Sentiment)
			gl, pr = append(gl, l), append(pr, res)
			gt, pt = append(gt, l.Topics), append(pt, res.Topics)
			gi, pi = append(gi, l.Intent), append(pi, res.Intent)
			ge, pe = append(ge, l.Emotion), append(pe, res.Emotion)
			gla, pla = append(gla, l.Lang), append(pla, res.Lang)
			if l.Lang == "ar" {
				gd, pd = append(gd, l.Dialect), append(pd, res.Dialect)
			}
			tox, toxP = append(tox, l.Toxicity != "" && l.Toxicity != "none"), append(toxP, res.Toxicity != "none")
			sar, sarP = append(sar, l.Sarcasm), append(sarP, res.Sarcasm)
			spam, spamP = append(spam, l.Spam), append(spamP, res.Spam)
			urg, urgP = append(urg, l.Urgent), append(urgP, res.Urgent)
			ad, adP = append(ad, l.Ad != ""), append(adP, res.AdType != "")
			if l.Ad != "" && res.AdType != "" {
				adT, adTP = append(adT, l.Ad), append(adTP, res.AdType)
			}
			leg, legP = append(leg, len(l.Legal) > 0), append(legP, len(res.Legal) > 0)
			setCounts(l.Legal, res.Legal, &legCat)
		}
	}
	s.Items = predSlot
	if predSlot > 0 {
		s.Coverage = float64(covered) / float64(predSlot)
	}
	if len(reqs) > 0 {
		s.RefusalRate = float64(refused) / float64(len(reqs))
		s.ErrorRate = float64(errored) / float64(len(reqs))
	}
	if len(gs) > 0 {
		s.SentF1 = MacroF1(gs, ps)
		s.SentCI[0], s.SentCI[1] = Bootstrap(gs, ps, MacroF1, 1000)
		s.Sent3F1 = MacroF1(Collapse3(gs), Collapse3(ps))
		s.Entities = ScoreEntities(gl, pr)
		s.Topics = ScoreTopics(gt, pt, 5)
		s.IntentF1 = MacroF1(gi, pi)
		s.EmotionAcc = Accuracy(ge, pe)
		s.LangAcc = Accuracy(gla, pla)
		s.DialectAcc = Accuracy(gd, pd)
		s.Toxic, s.Sarcasm, s.Spam, s.Urgent = BinaryF1(tox, toxP), BinaryF1(sar, sarP), BinaryF1(spam, spamP), BinaryF1(urg, urgP)
		s.Ad, s.Legal = BinaryF1(ad, adP), BinaryF1(leg, legP)
		s.AdTypeAcc = Accuracy(adT, adTP)
		s.LegalCat = prf(legCat[0], legCat[1], legCat[2])
	}
	s.LogoF1 = prf(logo[0], logo[1], logo[2])
	if ocrWant > 0 {
		s.OCRRecall = float64(ocrGot) / float64(ocrWant)
	}
	if textItems > 0 {
		s.USDPer1K = textCost / float64(textItems) * 1000
		s.TokensIn = float64(inTok) / float64(textItems)
		s.TokensCached = float64(cacheTok) / float64(textItems)
		s.TokensOut = float64(outTok) / float64(textItems)
	}
	if imgItems > 0 {
		s.ImageUSDPer1K = imgCost / float64(imgItems) * 1000
	}
	if comments > 0 {
		s.CostPerComment, s.CommentsCounted = commentCost/float64(comments), comments
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	if len(lat) > 0 {
		s.LatencyP50MS = lat[len(lat)/2]
		s.LatencyP95MS = lat[min(len(lat)-1, int(float64(len(lat))*0.95))]
	}
	return s
}

func scoreImage(l *Label, m *core.MediaResult, logo *[3]int, want, got *int) {
	gold := map[string]bool{}
	for _, x := range l.Logos {
		gold[textnorm.Normalize(x)] = true
	}
	matched := map[string]bool{}
	ocr := ""
	if m != nil {
		for _, x := range m.Logos {
			hit := ""
			for _, k := range []string{textnorm.Normalize(x.ID), textnorm.Normalize(x.Name)} {
				if k != "" && gold[k] {
					hit = k
					break
				}
			}
			if hit != "" && !matched[hit] {
				matched[hit] = true
				logo[0]++
			} else if hit == "" {
				logo[1]++
			}
		}
		ocr = textnorm.Normalize(m.TextInImage)
	}
	logo[2] += len(gold) - len(matched)
	for _, w := range l.OCRContains {
		*want++
		if strings.Contains(ocr, textnorm.Normalize(w)) {
			*got++
		}
	}
}

// setCounts adds a set comparison to TP/FP/FN counts.
func setCounts(gold, pred []string, c *[3]int) {
	g := map[string]bool{}
	for _, x := range gold {
		g[x] = true
	}
	p := map[string]bool{}
	for _, x := range pred {
		if p[x] {
			continue
		}
		p[x] = true
		if g[x] {
			c[0]++
		} else {
			c[1]++
		}
	}
	for x := range g {
		if !p[x] {
			c[2]++
		}
	}
}

// WriteReport prints the comparison as Markdown tables.
func WriteReport(w io.Writer, scores []Scores) {
	pct := func(x float64) string { return fmt.Sprintf("%.1f", 100*x) }
	_, _ = fmt.Fprintln(w, "| Configuration | Items | Coverage % | Sentiment macro-F1 (95% CI) | 3-class F1 | Aspect F1 | Tracked aspect F1 | Entity F1 | Linking % | Topic micro-F1 | Top-level topic F1 | Intent F1 | Lang % | Dialect % |")
	_, _ = fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, s := range scores {
		if s.Items == 0 {
			continue
		}
		_, _ = fmt.Fprintf(w, "| %s | %d | %s | %s (%s–%s) | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", s.Config, s.Items, pct(s.Coverage),
			pct(s.SentF1), pct(s.SentCI[0]), pct(s.SentCI[1]), pct(s.Sent3F1), pct(s.Entities.Aspect.F1), pct(s.Entities.TrackedAspect.F1),
			pct(s.Entities.Entities.F1), pct(s.Entities.Linking), pct(s.Topics.Micro.F1), pct(s.Topics.TopLevel.F1), pct(s.IntentF1),
			pct(s.LangAcc), pct(s.DialectAcc))
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "| Configuration | Toxic F1 | Sarcasm F1 | Spam F1 | Urgent F1 | Ad F1 | Ad type % | Legal F1 (any) | Legal category F1 | Emotion % | USD / 1k items | Tokens in / cached / out per item | Latency p50 / p95 ms | Refusals % | Errors % |")
	_, _ = fmt.Fprintln(w, "|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|")
	for _, s := range scores {
		if s.Items == 0 {
			continue
		}
		_, _ = fmt.Fprintf(w, "| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | $%.4f | %.0f / %.0f / %.0f | %d / %d | %s | %s |\n", s.Config,
			pct(s.Toxic.F1), pct(s.Sarcasm.F1), pct(s.Spam.F1), pct(s.Urgent.F1), pct(s.Ad.F1), pct(s.AdTypeAcc), pct(s.Legal.F1),
			pct(s.LegalCat.F1), pct(s.EmotionAcc), s.USDPer1K,
			s.TokensIn, s.TokensCached, s.TokensOut, s.LatencyP50MS, s.LatencyP95MS, pct(s.RefusalRate), pct(s.ErrorRate))
	}
	images := false
	for _, s := range scores {
		images = images || s.Images > 0
	}
	if images {
		_, _ = fmt.Fprintln(w)
		_, _ = fmt.Fprintln(w, "| Configuration | Images | Logo F1 | OCR keyword recall % | USD / 1k images |")
		_, _ = fmt.Fprintln(w, "|---|---|---|---|---|")
		for _, s := range scores {
			if s.Images > 0 {
				_, _ = fmt.Fprintf(w, "| %s | %d | %s | %s | $%.3f |\n", s.Config, s.Images, pct(s.LogoF1.F1), pct(s.OCRRecall), s.ImageUSDPer1K)
			}
		}
	}
}
