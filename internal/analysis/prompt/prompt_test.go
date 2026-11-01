package prompt

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/entities"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/taxonomy"
)

func tax(t *testing.T) *taxonomy.Taxonomy {
	t.Helper()
	tx, err := taxonomy.Load("")
	if err != nil {
		t.Fatal(err)
	}
	return tx
}

func reg() *entities.Registry {
	return entities.New([]entities.Entity{{ID: "asiacell", Name: "Asiacell", NameAR: "آسياسيل", Type: "brand"}},
		[]entities.Alias{{Alias: "asia cell", EntityID: "asiacell"}, {Alias: "آسيا سيل", EntityID: "asiacell"}}, nil)
}

// decode returns a schema as the providers receive it (JSON).
func decode(t *testing.T, s map[string]any) (map[string]any, string) {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out, string(b)
}

func TestSchemaIsStrict(t *testing.T) {
	for _, kw := range []bool{false, true} {
		s, raw := decode(t, TextSchema(tax(t), kw))
		item := s["properties"].(map[string]any)["items"].(map[string]any)["items"].(map[string]any)
		props := item["properties"].(map[string]any)
		req := item["required"].([]any)
		if len(props) != len(req) || item["additionalProperties"] != false {
			t.Fatalf("every property must be required and no others allowed: %d props, %d required", len(props), len(req))
		}
		for _, k := range req {
			if _, ok := props[k.(string)]; !ok {
				t.Fatalf("required %s missing from properties", k)
			}
		}
		if _, ok := props["kw"]; ok != kw {
			t.Fatalf("kw in schema = %v, want %v", ok, kw)
		}
		topics := props["topics"].(map[string]any)["items"].(map[string]any)["enum"].([]any)
		if len(topics) != tax(t).Len() {
			t.Fatal("topic enum must list the taxonomy")
		}
		// Fields keep the declared order: the index first, every label
		// before its score and confidence.
		order := []string{"i", "lng", "sent", "score", "conf", "ents", "topics", "new_topics", "intent", "emotion", "tox", "flags", "ppl"}
		for k := 1; k < len(order); k++ {
			if strings.Index(raw, `"`+order[k-1]+`":{`) > strings.Index(raw, `"`+order[k]+`":{`) {
				t.Fatalf("schema properties out of order (%s after %s): %s", order[k-1], order[k], raw)
			}
		}
	}
	if _, raw := decode(t, MediaSchema(tax(t))); !strings.Contains(raw, `"properties":{"logos":`) {
		t.Fatalf("media schema order: %s", raw)
	}
}

func TestLangOf(t *testing.T) {
	for _, c := range LangCodes {
		lang, dialect, ok := LangOf(c)
		if !ok || !oneOf(lang, Langs) || !oneOf(dialect, Dialects) {
			t.Fatalf("%s -> %s/%s/%v", c, lang, dialect, ok)
		}
	}
	if l, d, _ := LangOf("ar-iq"); l != "ar" || d != "iraqi" {
		t.Fatal("ar-iq")
	}
	if l, d, ok := LangOf("xx"); ok || l != "other" || d != "none" {
		t.Fatal("unknown code")
	}
}

func TestPercent(t *testing.T) {
	for in, want := range map[float64]float64{-80: -0.8, 100: 1, 1: 0.01, 0: 0, 0.85: 0.85, -0.5: -0.5, 250: 2.5} {
		if got := percent(in); got != want {
			t.Errorf("percent(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestSystemPromptIsStable(t *testing.T) {
	a, b := TextSystem(tax(t)), TextSystem(tax(t))
	if a != b || !strings.Contains(a, "telecom.network:") || !strings.Contains(MediaSystem(tax(t)), "Never identify anyone") {
		t.Fatal("system prompt must be byte-identical across calls and contain the taxonomy")
	}
}

func TestTextUser(t *testing.T) {
	u := TextUser(Context{Page: "Asiacell", PageHints: []Hint{{ID: "asiacell", Name: "Asiacell"}}, Post: "عرض"}, []ItemInput{
		{I: 0, Kind: "comment", Text: "حلو", ReplyTo: -1},
		{I: 1, Kind: "reply", Text: "صح", ReplyTo: 0},
		{I: 2, Kind: "reply", Text: "لا", ReplyTo: -1, ParentText: "سؤال", Hints: []Hint{{ID: "x", Name: "X"}}},
	})
	for _, want := range []string{"page: Asiacell", `page_hints: [{"id":"asiacell"`, "post: عرض", `"reply_to":0`, `"parent_text":"سؤال"`, `"hints":[{"id":"x"`} {
		if !strings.Contains(u, want) {
			t.Errorf("user message lacks %s:\n%s", want, u)
		}
	}
	if strings.Count(u, "reply_to") != 1 {
		t.Fatal("reply_to only for in-group parents")
	}
	if strings.Contains(TextUser(Context{}, nil), "<context>") {
		t.Fatal("empty context rendered")
	}
}

const good = `{"items":[
 {"i":0,"lng":"ar-iq","sent":"negative","score":-80,"conf":90,
  "ents":[{"name":"اسيا سيل","type":"brand","id":"","sent":"negative","score":-90,"aspect":"network"},
          {"name":"Ali","type":"person_private","id":"","sent":"neutral","score":0,"aspect":"general"},
          {"name":"Asiacell","type":"brand","id":"asiacell","sent":"negative","score":-90,"aspect":"network"}],
  "topics":["telecom.network","made.up","telecom.network","telecom.outage","telecom.pricing","other"],
  "new_topics":[{"en":" Water shortage ","ar":"شح المياه","parent":"services"},{"en":"","ar":"","parent":"services"},
    {"en":"Fuel queues","ar":"","parent":"made.up"},{"en":"c","ar":"","parent":"other"},{"en":"d","ar":"","parent":"other"}],
  "kw":["نت","","a","b","c","d","e"],"intent":"complaint","emotion":"anger","tox":"none",
  "flags":["sarcasm","urgent","cs","bogus","ad_own","ad_hidden","ad_sale","fraud_scam","incitement","fraud_scam","misinformation"],"ppl":1},
 {"i":1,"lng":"klingon","sent":"positive","score":300,"conf":-1,"ents":[],
  "topics":[],"intent":"praise","emotion":"joy","tox":"none","flags":["spam","hate","threat","harass"],"ppl":-2},
 {"i":1,"lng":"ar","sent":"neutral"},
 {"i":7,"lng":"ar","sent":"neutral"},
 {"i":2,"lng":"ar","sent":"angry"}
]}`

func TestParseText(t *testing.T) {
	got, missing, p, err := ParseText("```json\n"+good+"\n```", []int{0, 1, 2, 3}, tax(t), reg())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || len(missing) != 2 || missing[0] != 2 || missing[1] != 3 {
		t.Fatalf("got %d, missing %v", len(got), missing)
	}
	r := got[0]
	if r.Sentiment != "negative" || !r.Sarcasm || !r.Urgent || !r.CodeSwitch || r.Spam || r.Intent != "complaint" ||
		r.Lang != "ar" || r.Dialect != "iraqi" || r.Score != -0.8 || r.Confidence != 0.9 || r.Entities == nil {
		t.Fatalf("result %+v", r)
	}
	// The surface form and the id resolve to the same entity: one mention.
	if len(r.Entities) != 1 || r.Entities[0].ID != "asiacell" || r.Entities[0].Name != "Asiacell" || r.Entities[0].Aspect != "network" ||
		r.Entities[0].Score != -0.9 {
		t.Fatalf("entities %+v", r.Entities)
	}
	if r.PrivatePersonMentions != 2 || p.PrivatePersons != 1 {
		t.Fatalf("private persons %d / %d", r.PrivatePersonMentions, p.PrivatePersons)
	}
	if strings.Join(r.Topics, ",") != "telecom.network,telecom.outage,telecom.pricing,other" || p.InvalidTopics != 1 {
		t.Fatalf("topics %v (invalid %d)", r.Topics, p.InvalidTopics)
	}
	if len(r.Keywords) != MaxKeywords || r.Keywords[0] != "نت" {
		t.Fatalf("keywords %v", r.Keywords)
	}
	// The most specific ad type wins; breaches listed once each.
	if r.AdType != "hidden" || strings.Join(r.Legal, ",") != "fraud_scam,incitement,misinformation" {
		t.Fatalf("ad %q legal %v", r.AdType, r.Legal)
	}
	// Proposals: trimmed, empty ones dropped, unknown parent -> other, at
	// most MaxNewTopics.
	if len(r.NewTopics) != MaxNewTopics || r.NewTopics[0].EN != "Water shortage" || r.NewTopics[0].Parent != "services" ||
		r.NewTopics[1].Parent != "other" {
		t.Fatalf("new topics %+v", r.NewTopics)
	}
	r1 := got[1]
	if r1.Lang != "other" || r1.Dialect != "none" || r1.Score != 1 || r1.Confidence != 0 || r1.PrivatePersonMentions != 0 ||
		!r1.Spam || !r1.Hate || !r1.Threat || !r1.Harassment || r1.Sarcasm || len(r1.Keywords) != 0 || r1.AdType != "" ||
		strings.Join(r1.Legal, ",") != "hate_speech,threat" || r1.NewTopics != nil {
		t.Fatalf("repairs %+v", r1)
	}
	if p.DuplicateItems != 1 || p.UnexpectedItems != 1 || p.FixedValues < 4 {
		t.Fatalf("problems %+v", p)
	}
	if r.CalibratedConfidence != r.Confidence {
		t.Fatal("calibrated defaults to raw")
	}
}

func TestNewTopicParents(t *testing.T) {
	got := NewTopics([]newTopic{
		{EN: "Electricity cuts", Parent: "services.electricity"}, // a topic: a sub-topic under it
		{EN: "Night cuts", Parent: "services.electricity.cuts"},  // below a sub-topic: moved up
		{EN: "Something", Parent: "nowhere.at.all"},              // unknown: other
	}, tax(t))
	if len(got) != 3 || got[0].Parent != "services.electricity" || got[1].Parent != "services.electricity" || got[2].Parent != "other" {
		t.Fatalf("parents %+v", got)
	}
}

func TestAffiliateWins(t *testing.T) {
	raw := `{"items":[{"i":0,"lng":"ar-iq","sent":"positive","score":50,"conf":80,"ents":[],"topics":["other"],"new_topics":[],
		"intent":"recommendation","emotion":"joy","tox":"none","flags":["ad_hidden","religious_offense","ad_affiliate","extortion","other_illegal"],"ppl":0}]}`
	got, _, _, err := ParseText(raw, []int{0}, tax(t), reg())
	if err != nil {
		t.Fatal(err)
	}
	if r := got[0]; r.AdType != "affiliate" || strings.Join(r.Legal, ",") != "religious_offense,extortion,other_illegal" {
		t.Fatalf("ad %q legal %v", r.AdType, r.Legal)
	}
	for _, c := range []string{"hate_speech", "threat", "misinformation", "defamation"} {
		if !oneOf(c, LegalCategories) {
			t.Fatalf("%s missing from LegalCategories", c)
		}
	}
}

// Every breach flag the schema allows is defined for the model, and
// insulting religion is always flagged (the owner's instruction).
func TestLegalFlagsDefined(t *testing.T) {
	sys := TextSystem(tax(t))
	for _, f := range LegalFlags {
		if !strings.Contains(sys, "\n  - "+f+": ") {
			t.Errorf("%s has no definition in the text instructions", f)
		}
	}
	if !strings.Contains(sys, "religious_offense: insults") || !strings.Contains(sys, "Always flag these, whoever the target") {
		t.Error("religious_offense must be defined as always flagged")
	}
}

func TestFinish(t *testing.T) {
	r := core.Result{Keywords: []string{"نت"}}
	Finish(&r, "النت bad")
	if r.Script != "mixed" || len(r.Keywords) != 1 {
		t.Fatalf("model keywords are kept: %+v", r)
	}
	r = core.Result{}
	Finish(&r, "الشبكة ضعيفة اتصل [phone]")
	if r.Script != "arabic" || strings.Join(r.Keywords, " ") != "الشبكة ضعيفة اتصل" {
		t.Fatalf("local keywords: %+v", r)
	}
	r = core.Result{Refused: true}
	Finish(&r, "نص طويل هنا")
	if len(r.Keywords) != 0 {
		t.Fatal("no keywords for a refused item")
	}
}

func TestParseTextInvalid(t *testing.T) {
	for _, raw := range []string{"", "no json here", "{not json}", `{"items": 5}`} {
		_, missing, _, err := ParseText(raw, []int{0, 1}, tax(t), reg())
		if err == nil || len(missing) != 2 {
			t.Errorf("%q: err %v missing %v", raw, err, missing)
		}
	}
	got, missing, _, err := ParseText(`{"items":[]}`, []int{0}, tax(t), reg())
	if err != nil || len(got) != 0 || len(missing) != 1 {
		t.Fatal("empty items list")
	}
}

func TestParseMedia(t *testing.T) {
	raw := `{"logos":[{"name":"آسياسيل","id":"","conf":90},{"name":"Asiacell","id":"asiacell","conf":200},{"name":"","id":"","conf":100},{"name":"Pepsi","id":"","conf":50}],
		"ocr":"` + strings.Repeat("ا", 700) + `","desc":"A billboard.","labels":["sign","street","a","b","c","d","e","f","g"],
		"people":-1,"sent":"bogus","score":20,"conf":70,"flags":["ad_paid","weapons","cs"],"ents":[{"name":"Ali","type":"person_private","id":"","sent":"neutral","score":0,"aspect":"general"}],"topics":["telecom.pricing"]}`
	m, _, err := ParseMedia(raw, tax(t), reg())
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Logos) != 2 || m.Logos[0].ID != "asiacell" || m.Logos[1].Name != "Pepsi" || m.Logos[1].ID != "" {
		t.Fatalf("logos %+v", m.Logos)
	}
	if len([]rune(m.TextInImage)) != 500 || len(m.Labels) != 8 || m.PeopleCount != 0 || m.Sentiment != "neutral" || len(m.Entities) != 0 ||
		m.Score != 0.2 || m.Confidence != 0.7 || m.Logos[0].Confidence != 0.9 || m.Logos[1].Confidence != 0.5 ||
		m.AdType != "paid" || strings.Join(m.Legal, ",") != "weapons" {
		t.Fatalf("media %+v", m)
	}
	if _, _, err := ParseMedia("nope", tax(t), reg()); err == nil {
		t.Fatal("invalid media answer accepted")
	}
}

func TestParseCuration(t *testing.T) {
	cands := []CurationCandidate{{ID: "services.water_shortage"}, {ID: "services.power_cuts"}, {ID: "other.asiacell"}, {ID: "x.y"}}
	raw := `{"decisions":[
	 {"id":"services.water_shortage","action":"approve","into":"","en":" Water shortage ","ar":"شح المياه","parent":"services"},
	 {"id":"services.power_cuts","action":"merge","into":"services.electricity","en":"","ar":"","parent":"services"},
	 {"id":"other.asiacell","action":"reject","into":"","en":"","ar":"","parent":"other"},
	 {"id":"x.y","action":"merge","into":"made.up","en":"","ar":"","parent":"other"},
	 {"id":"unknown.topic","action":"approve","into":"","en":"X","ar":"","parent":"other"},
	 {"id":"services.water_shortage","action":"reject","into":"","en":"","ar":"","parent":"other"}]}`
	ds, err := ParseCuration(raw, tax(t), cands)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 3 || ds[0].EN != "Water shortage" || ds[1].Into != "services.electricity" || ds[2].Action != "reject" {
		t.Fatalf("decisions %+v", ds)
	}
	if !strings.Contains(CurationUser(tax(t), cands), "services.power_cuts") {
		t.Fatal("candidates missing from the request")
	}
	if _, err := json.Marshal(CurationSchema(tax(t))); err != nil {
		t.Fatal(err)
	}
}

// An approval naming a known entity's id is a spelling of it: merged, not
// a second entity.
func TestEntityApprovalOfKnownIDMerges(t *testing.T) {
	cands := []EntityCandidate{{Key: "0", Name: "Asia Cell Iraq", Type: "brand"}, {Key: "1", Name: "Toyota Iraq", Type: "brand"}}
	raw := `{"decisions":[{"key":"0","action":"approve","into":"","id":"asiacell","name":"Asiacell","name_ar":"","type":"brand","public_figure":false},
		{"key":"1","action":"approve","into":"","id":"toyota-iraq","name":"Toyota Iraq","name_ar":"","type":"brand","public_figure":false}]}`
	ds, err := ParseEntityCuration(raw, cands, func(id string) bool { return id == "asiacell" })
	if err != nil || len(ds) != 2 {
		t.Fatalf("%+v %v", ds, err)
	}
	if ds[0].Action != "merge" || ds[0].Into != "asiacell" || ds[1].Action != "approve" || ds[1].ID != "toyota-iraq" {
		t.Fatalf("decisions %+v", ds)
	}
}
