package prompt

import (
	"encoding/json"
	"strings"
	"testing"

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

func TestSchemaIsStrict(t *testing.T) {
	s := TextSchema(tax(t))
	item := s["properties"].(map[string]any)["items"].(map[string]any)["items"].(map[string]any)
	props := item["properties"].(map[string]any)
	req := item["required"].([]string)
	if len(props) != len(req) || item["additionalProperties"] != false {
		t.Fatalf("every property must be required and no others allowed: %d props, %d required", len(props), len(req))
	}
	for _, k := range req {
		if _, ok := props[k]; !ok {
			t.Fatalf("required %s missing from properties", k)
		}
	}
	topics := props["topics"].(map[string]any)["items"].(map[string]any)["enum"].([]string)
	if len(topics) != tax(t).Len() {
		t.Fatal("topic enum must list the taxonomy")
	}
	if _, err := json.Marshal(MediaSchema(tax(t))); err != nil {
		t.Fatal(err)
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
 {"i":0,"lang":"ar","dialect":"iraqi","script":"arabic","cs":false,"sent":"negative","score":-0.8,"conf":0.9,
  "ents":[{"name":"اسيا سيل","type":"brand","id":"","sent":"negative","score":-0.9,"aspect":"network"},
          {"name":"Ali","type":"person_private","id":"","sent":"neutral","score":0,"aspect":"general"},
          {"name":"Asiacell","type":"brand","id":"asiacell","sent":"negative","score":-0.9,"aspect":"network"}],
  "topics":["telecom.network","made.up","telecom.network","telecom.outage","telecom.pricing","other"],
  "kw":["نت","","a","b","c","d","e"],"intent":"complaint","emotion":"anger","tox":"none","hate":false,"threat":false,
  "harass":false,"spam":false,"sarcasm":true,"urgent":true,"ppl":1},
 {"i":1,"lang":"klingon","dialect":"x","script":"arabic","cs":false,"sent":"positive","score":3,"conf":-1,"ents":[],
  "topics":[],"kw":[],"intent":"praise","emotion":"joy","tox":"none","hate":false,"threat":false,"harass":false,
  "spam":false,"sarcasm":false,"urgent":false,"ppl":-2},
 {"i":1,"lang":"ar","sent":"neutral"},
 {"i":7,"lang":"ar","sent":"neutral"},
 {"i":2,"lang":"ar","sent":"angry"}
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
	if r.Sentiment != "negative" || !r.Sarcasm || !r.Urgent || r.Intent != "complaint" {
		t.Fatalf("result %+v", r)
	}
	// The surface form and the id resolve to the same entity: one mention.
	if len(r.Entities) != 1 || r.Entities[0].ID != "asiacell" || r.Entities[0].Name != "Asiacell" || r.Entities[0].Aspect != "network" {
		t.Fatalf("entities %+v", r.Entities)
	}
	if r.PrivatePersonMentions != 2 || p.PrivatePersons != 1 {
		t.Fatalf("private persons %d / %d", r.PrivatePersonMentions, p.PrivatePersons)
	}
	if strings.Join(r.Topics, ",") != "telecom.network,telecom.outage,telecom.pricing" || p.InvalidTopics != 1 {
		t.Fatalf("topics %v (invalid %d)", r.Topics, p.InvalidTopics)
	}
	if len(r.Keywords) != MaxKeywords || r.Keywords[0] != "نت" {
		t.Fatalf("keywords %v", r.Keywords)
	}
	r1 := got[1]
	if r1.Lang != "other" || r1.Dialect != "none" || r1.Score != 1 || r1.Confidence != 0 || r1.PrivatePersonMentions != 0 {
		t.Fatalf("repairs %+v", r1)
	}
	if p.DuplicateItems != 1 || p.UnexpectedItems != 1 || p.FixedValues < 4 {
		t.Fatalf("problems %+v", p)
	}
	if r.CalibratedConfidence != r.Confidence {
		t.Fatal("calibrated defaults to raw")
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
	raw := `{"logos":[{"name":"آسياسيل","id":"","conf":0.9},{"name":"Asiacell","id":"asiacell","conf":2},{"name":"","id":"","conf":1},{"name":"Pepsi","id":"","conf":0.5}],
		"ocr":"` + strings.Repeat("ا", 700) + `","desc":"A billboard.","labels":["sign","street","a","b","c","d","e","f","g"],
		"people":-1,"sent":"bogus","score":0.2,"conf":0.7,"ents":[{"name":"Ali","type":"person_private","id":"","sent":"neutral","score":0,"aspect":"general"}],"topics":["telecom.pricing"]}`
	m, _, err := ParseMedia(raw, tax(t), reg())
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Logos) != 2 || m.Logos[0].ID != "asiacell" || m.Logos[1].Name != "Pepsi" || m.Logos[1].ID != "" {
		t.Fatalf("logos %+v", m.Logos)
	}
	if len([]rune(m.TextInImage)) != 500 || len(m.Labels) != 8 || m.PeopleCount != 0 || m.Sentiment != "neutral" || len(m.Entities) != 0 {
		t.Fatalf("media %+v", m)
	}
	if _, _, err := ParseMedia("nope", tax(t), reg()); err == nil {
		t.Fatal("invalid media answer accepted")
	}
}
