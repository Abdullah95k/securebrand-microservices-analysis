package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/entities"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider/fake"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/provider/registry"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/taxonomy"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/textnorm"
)

const synthetic = "../../testdata/analysis/synthetic-gold-example.jsonl"

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestMacroF1AndAccuracy(t *testing.T) {
	gold := []string{"pos", "pos", "neg", "neg", "neu"}
	pred := []string{"pos", "neg", "neg", "neg", "pos"}
	// pos: P 1/2 R 1/2 F 0.5; neg: P 2/3 R 1 F 0.8; neu: F 0 -> mean 0.4333
	if got := MacroF1(gold, pred); !near(got, (0.5+0.8+0)/3) {
		t.Fatalf("macro-F1 %v", got)
	}
	if !near(Accuracy(gold, pred), 0.6) || MacroF1(nil, nil) != 0 || Accuracy(nil, nil) != 0 {
		t.Fatal("accuracy")
	}
	lo, hi := Bootstrap(gold, pred, MacroF1, 500)
	if lo > MacroF1(gold, pred) || hi < MacroF1(gold, pred) || lo < 0 || hi > 1 {
		t.Fatalf("bootstrap [%v, %v]", lo, hi)
	}
	if lo2, hi2 := Bootstrap(gold, pred, MacroF1, 500); lo2 != lo || hi2 != hi {
		t.Fatal("bootstrap must be reproducible")
	}
	if c := Collapse3([]string{"mixed", "positive"}); c[0] != "neutral" || c[1] != "positive" {
		t.Fatal("collapse")
	}
	b := BinaryF1([]bool{true, true, false, false}, []bool{true, false, true, false})
	if !near(b.P, 0.5) || !near(b.R, 0.5) || !near(b.F1, 0.5) {
		t.Fatalf("binary %+v", b)
	}
}

func TestScoreEntities(t *testing.T) {
	gold := []Label{
		{Entities: []Entity{{Name: "اسياسيل", Type: "brand", ID: "asiacell", Sentiment: "negative"}, {Name: "بغداد", Type: "place", Sentiment: "neutral"}}},
		{Entities: []Entity{{Name: "كورك", Type: "brand", ID: "korek", Sentiment: "positive"}, {Name: "Ali", Type: "person_private", Sentiment: "neutral"}}},
	}
	pred := []core.Result{
		{Entities: []core.Entity{{ID: "asiacell", Name: "Asiacell", Type: "brand", Sentiment: "negative"}, {Name: "بغداد", Type: "place", Sentiment: "negative"}}},
		{Entities: []core.Entity{{Name: "كورك", Type: "brand", Sentiment: "positive"}, {Name: "Zain", Type: "brand", ID: "zain-iraq", Sentiment: "neutral"}}},
	}
	s := ScoreEntities(gold, pred)
	// Entities: 3 gold (private person ignored), 4 predicted, 3 matched
	// (korek matched by name although unlinked).
	if s.Entities.TP != 3 || s.Entities.FP != 1 || s.Entities.FN != 0 {
		t.Fatalf("entities %+v", s.Entities)
	}
	// Aspect pairs: asiacell/neg and korek/pos right, baghdad wrong.
	if s.Aspect.TP != 2 || !near(s.AspectAccuracy, 2.0/3) {
		t.Fatalf("aspect %+v acc %v", s.Aspect, s.AspectAccuracy)
	}
	// Linking: 2 gold ids, only asiacell's found.
	if !near(s.Linking, 0.5) {
		t.Fatalf("linking %v", s.Linking)
	}
	if s.TrackedAspect.TP != 2 || s.TrackedAspect.FP != 1 {
		t.Fatalf("tracked %+v", s.TrackedAspect)
	}
}

func TestScoreTopics(t *testing.T) {
	gold := [][]string{{"telecom.network"}, {"telecom.pricing", "brand.giveaway"}, {"other"}}
	pred := [][]string{{"telecom.outage"}, {"telecom.pricing"}, {"other", "social.weather"}}
	s := ScoreTopics(gold, pred, 1)
	if s.Micro.TP != 2 || s.Micro.FP != 2 || s.Micro.FN != 2 {
		t.Fatalf("micro %+v", s.Micro)
	}
	if s.TopLevel.TP != 3 { // telecom, telecom, other
		t.Fatalf("top level %+v", s.TopLevel)
	}
	if s.Macro <= 0 || s.Macro >= 1 {
		t.Fatalf("macro %v", s.Macro)
	}
}

func TestCalibrateIsMonotone(t *testing.T) {
	var gold []Item
	var preds []Prediction
	confs := []float64{0.95, 0.9, 0.9, 0.8, 0.7, 0.6, 0.6, 0.5}
	right := []bool{true, true, false, true, false, true, false, false}
	for i := range confs {
		id := string(rune('a' + i))
		gold = append(gold, Item{ID: id, Label: &Label{Sentiment: "positive"}})
		s := "negative"
		if right[i] {
			s = "positive"
		}
		preds = append(preds, Prediction{ID: id, Result: &core.Result{Sentiment: s, Confidence: confs[i]}})
	}
	pts := Calibrate(gold, []Request{{Items: preds}})
	if len(pts) == 0 {
		t.Fatal("no points")
	}
	for i := 1; i < len(pts); i++ {
		if pts[i][0] < pts[i-1][0] || pts[i][1] < pts[i-1][1] {
			t.Fatalf("not monotone: %v", pts)
		}
	}
	if Calibrate(gold, nil) != nil {
		t.Fatal("calibration without results")
	}
}

func testRunner(t *testing.T, out *bytes.Buffer, fp *fake.Provider) *Runner {
	t.Helper()
	tax, err := taxonomy.Load("")
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := entities.LoadSeed("")
	var ents []entities.Entity
	var aliases []entities.Alias
	for _, e := range seed.Entities {
		ents = append(ents, entities.Entity{ID: e.ID, Name: e.Name, NameAR: e.NameAR, Type: e.Type})
		for _, a := range e.Aliases {
			aliases = append(aliases, entities.Alias{Alias: a, EntityID: e.ID})
		}
	}
	reg, _ := registry.New(nil)
	reg.Add("fake", fp, true, 0)
	full, _ := config.Load("")
	full.Analysis.Pricing["fake-model"] = config.ModelPrice{Input: 1, Output: 2, BatchDiscount: 0.5}
	return &Runner{Providers: reg, Tax: tax, Entities: entities.New(ents, aliases, nil), Pricing: provider.Pricing(full.Analysis.Pricing),
		Out: json.NewEncoder(out), Poll: 1}
}

func TestRunAndReportOnSyntheticGold(t *testing.T) {
	items, err := ReadItems(synthetic)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 11 || len(Labelled(items)) != 9 {
		t.Fatalf("synthetic gold: %d items, %d labelled", len(items), len(Labelled(items)))
	}
	var out bytes.Buffer
	fp := fake.New("fake")
	fp.Drop = func(i int) bool { return i == 2 } // one missing item per request
	r := testRunner(t, &out, fp)
	for _, batch := range []bool{false, true} {
		if err := r.Run(context.Background(), Labelled(items), RunConfig{Provider: "fake", Model: "fake-model", GroupSize: 4, Thinking: "off", Batch: batch}); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "results.jsonl")
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	reqs, err := ReadRequests(path)
	if err != nil {
		t.Fatal(err)
	}
	scores := Score(items, reqs)
	if len(scores) != 2 {
		t.Fatalf("configs %d", len(scores))
	}
	for _, s := range scores {
		if s.Items != 9 || s.Coverage >= 1 || s.Coverage < 0.5 || s.SentF1 <= 0 || s.USDPer1K <= 0 || s.TokensOut <= 0 {
			t.Fatalf("scores %+v", s)
		}
	}
	// The script is computed from the text.
	text := map[string]string{}
	for _, it := range items {
		text[it.ID] = it.Text
	}
	for _, rq := range reqs {
		for _, pr := range rq.Items {
			if pr.Result != nil && pr.Result.Script != textnorm.Script(text[pr.ID]) {
				t.Fatalf("%s: script %q", pr.ID, pr.Result.Script)
			}
		}
	}
	// The fake links Asiacell through the alias hint: entity scoring sees it.
	if scores[0].Entities.Entities.TP == 0 {
		t.Fatalf("entities %+v", scores[0].Entities)
	}
	var rep bytes.Buffer
	WriteReport(&rep, scores)
	for _, want := range []string{"Sentiment macro-F1", "fake/fake-model", "USD / 1k items"} {
		if !strings.Contains(rep.String(), want) {
			t.Fatalf("report lacks %q:\n%s", want, rep.String())
		}
	}
	// Batch requests cost half.
	if !(scores[0].USDPer1K < scores[1].USDPer1K || scores[1].USDPer1K < scores[0].USDPer1K) {
		t.Fatal("batch and real-time costs should differ")
	}
}

func TestWriteItemsRoundTrip(t *testing.T) {
	items, _ := ReadItems(synthetic)
	path := filepath.Join(t.TempDir(), "g.labels.jsonl")
	if err := WriteItems(path, items); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", st.Mode(), err)
	}
	back, err := ReadItems(path)
	if err != nil || len(back) != len(items) || back[0].Label.Entities[0].ID != "asiacell" {
		t.Fatalf("round trip %v", err)
	}
	if GoldID("fb:comment:1") == GoldID("fb:comment:2") || !strings.HasPrefix(GoldID("x"), "g-") {
		t.Fatal("gold ids")
	}
}

func TestLabelServerSaves(t *testing.T) {
	items, _ := ReadItems(synthetic)
	path := filepath.Join(t.TempDir(), "todo.labels.jsonl")
	if err := WriteItems(path, items); err != nil {
		t.Fatal(err)
	}
	tax, _ := taxonomy.Load("")
	seed, _ := entities.LoadSeed("")
	s, err := NewLabelServer(path, tax, seed.Entities)
	if err != nil {
		t.Fatal(err)
	}
	// The first unlabelled item is shown.
	rec := httptest.NewRecorder()
	s.show(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "راح ابدل لزين العراق") || !strings.Contains(rec.Body.String(), `value="asiacell"`) {
		t.Fatalf("page %d", rec.Code)
	}
	form := url.Values{"i": {"10"}, "lang": {"ar"}, "dialect": {"iraqi"}, "sentiment": {"negative"}, "intent": {"churn_threat"},
		"emotion": {"anger"}, "toxicity": {"none"}, "topics": {"telecom.pricing", "telecom.network", "other", "social.youth"},
		"ad": {"hidden"}, "legal": {"fraud_scam", "defamation"},
		"e0_name": {"زين العراق"}, "e0_type": {"brand"}, "e0_id": {"zain-iraq"}, "e0_sent": {"positive"}, "e0_aspect": {"general"},
		"labeller": {"L1"}}
	req := httptest.NewRequest(http.MethodPost, "/save", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	s.save(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save %d %s", rec.Code, rec.Body.String())
	}
	back, _ := ReadItems(path)
	l := back[10].Label
	if l == nil || l.Sentiment != "negative" || len(l.Topics) != 4 || len(l.Entities) != 1 || l.Entities[0].ID != "zain-iraq" || l.Labeller != "L1" ||
		l.Ad != "hidden" || strings.Join(l.Legal, ",") != "fraud_scam,defamation" {
		t.Fatalf("saved label %+v", l)
	}
	// A label without sentiment is refused unless unsure.
	req = httptest.NewRequest(http.MethodPost, "/save", strings.NewReader(url.Values{"i": {"10"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	s.save(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatal("empty label accepted")
	}
	if err := s.Serve(context.Background(), "0.0.0.0:8099"); err == nil {
		t.Fatal("public listen address accepted")
	}
}

func TestCostModel(t *testing.T) {
	full, _ := config.Load("")
	p := provider.Pricing(full.Analysis.Pricing)
	lean, _ := MixByName("lean")
	whisper, _ := MixByName("lean+whisper")
	heavy, _ := MixByName("heavier")
	within := func(name string, got, lo, hi float64) {
		t.Helper()
		if got < lo || got > hi {
			t.Fatalf("%s: $%.0f, want %.0f..%.0f", name, got, lo, hi)
		}
	}
	// The first design's figures are reproduced with its assumptions.
	within("previous lean at 300M", Yearly(p, PreviousAssumptions, lean, 300e6).Total, 14800, 15400)
	within("previous heavier at 300M", Yearly(p, PreviousAssumptions, heavy, 300e6).Total, 47500, 49500)
	// Current defaults (design section 16).
	within("lean at 300M", Yearly(p, DefaultAssumptions, lean, 300e6).Total, 11400, 12000)
	within("lean+whisper at 300M", Yearly(p, DefaultAssumptions, whisper, 300e6).Total, 9500, 9900)
	within("lean+whisper at 1B", Yearly(p, DefaultAssumptions, whisper, 1e9).Total, 27400, 28200)
	b := Yearly(p, DefaultAssumptions, whisper, 1e9)
	if b.Lines["speech"] != 2000 {
		t.Fatalf("own Whisper is a fixed cost: %v", b.Lines["speech"])
	}
	// Duplicates are not sent: comments cost 10% less than without.
	noDup := DefaultAssumptions
	noDup.DuplicateShare = 0
	if r := Yearly(p, DefaultAssumptions, lean, 1e9).Lines["comments"] / Yearly(p, noDup, lean, 1e9).Lines["comments"]; r < 0.899 || r > 0.901 {
		t.Fatalf("duplicate share not applied: ratio %v", r)
	}
	within("haiku per 1M comments (batch)", 1e6*Unit(p, Choice{"claude-haiku-5-5", true}, DefaultAssumptions.Comment), 15.5, 16.5)
	var out bytes.Buffer
	WriteCostReport(&out, p, DefaultAssumptions, []float64{1e8, 3e8, 1e9})
	for _, want := range []string{"| lean | $3.9k | $11.7k | $38.9k |", "| lean+whisper | $4.6k | $9.7k | $27.8k |", "| speech | $1.3k \\| $2.0k |"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("cost report lacks %q:\n%s", want, out.String())
		}
	}
}

func TestRepeatsAndAgreement(t *testing.T) {
	items, _ := ReadItems(synthetic)
	with := AddRepeats(items, 0.5)
	if len(with) != len(items)+6 || !strings.HasSuffix(with[len(with)-1].ID, RepeatSuffix) || with[len(with)-1].Label != nil {
		t.Fatalf("repeats: %d", len(with))
	}
	if len(Labelled(with)) != len(Labelled(items)) {
		t.Fatal("repeats must not be scored")
	}
	// Label the copies: same as the original except one sentiment.
	for i := len(items); i < len(with); i++ {
		orig := with[i]
		for _, it := range items {
			if it.ID+RepeatSuffix == orig.ID && it.Label != nil {
				l := *it.Label
				with[i].Label = &l
			}
		}
	}
	with[len(items)].Label.Sentiment = "neutral" // syn-001 was negative
	a := SelfAgreement(with)
	if a.Pairs < 4 || a.SentimentAgree >= 1 || a.SentimentKappa >= 1 || a.SentimentKappa <= 0 || a.EntityF1 != 1 || a.TopicF1 != 1 {
		t.Fatalf("agreement %+v", a)
	}
	if !near(Kappa([]string{"a", "b"}, []string{"a", "b"}), 1) || Kappa(nil, nil) != 0 {
		t.Fatal("kappa")
	}
}
