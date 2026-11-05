package entities

import (
	"testing"
)

func testReg() *Registry {
	return New([]Entity{
		{ID: "asiacell", Name: "Asiacell", NameAR: "آسياسيل", Type: "brand", Tracked: true},
		{ID: "zain-iraq", Name: "Zain Iraq", NameAR: "زين العراق", Type: "brand"},
		{ID: "baghdad", Name: "Baghdad", NameAR: "بغداد", Type: "place"},
	}, []Alias{
		{Alias: "آسيا سيل", EntityID: "asiacell"},
		{Alias: "asia cell", EntityID: "asiacell"},
		{Alias: "شركة زين", EntityID: "zain-iraq"},
		{Alias: "x", EntityID: "missing"}, // alias of an unknown entity: ignored
	}, map[string]string{"old-asia": "asiacell"})
}

func TestFind(t *testing.T) {
	r := testReg()
	cases := map[string][]string{
		"شبكة اسياسيل خايسة":            {"asiacell"},
		"والاسياسيل كذلك":               {"asiacell"}, // clitics و + ال
		"بآسيا سيل ماكو نت":             {"asiacell"}, // clitic on a two-word alias
		"ASIA  CELL is down in Baghdad": {"asiacell", "baghdad"},
		"زين العراق وشركة زين":          {"zain-iraq"},
		"زين والله":                     nil, // "زين" alone is a word, not the brand
		"asiacellx":                     nil, // whole words only
		"":                              nil,
	}
	for text, want := range cases {
		got := r.Find(text)
		if len(got) != len(want) {
			t.Errorf("Find(%q) = %+v, want %v", text, got, want)
			continue
		}
		for i := range want {
			if got[i].EntityID != want[i] {
				t.Errorf("Find(%q)[%d] = %s, want %s", text, i, got[i].EntityID, want[i])
			}
		}
	}
	if m := r.Find("اسياسيل"); !m[0].Tracked || m[0].Name != "Asiacell" {
		t.Fatalf("tracked flag/name: %+v", m)
	}
	var nilReg *Registry
	if nilReg.Find("اسياسيل") != nil || nilReg.Link("asiacell", "") != "" {
		t.Fatal("nil registry")
	}
}

func TestLinkAndMerges(t *testing.T) {
	r := testReg()
	for _, c := range []struct{ id, name, want string }{
		{"asiacell", "whatever", "asiacell"},
		{"old-asia", "", "asiacell"}, // merged id followed
		{"made-up", "آسيا سيل", "asiacell"},
		{"", "بزين العراق", "zain-iraq"}, // clitic on the first word
		{"", "Korek", ""},
		{"", "", ""},
	} {
		if got := r.Link(c.id, c.name); got != c.want {
			t.Errorf("Link(%q, %q) = %q, want %q", c.id, c.name, got, c.want)
		}
	}
	if e, ok := r.Get("old-asia"); !ok || e.ID != "asiacell" {
		t.Fatal("Get must follow merges")
	}
	loop := New(nil, nil, map[string]string{"a": "b", "b": "a"})
	if _, ok := loop.Get("a"); ok {
		t.Fatal("merge loop resolved")
	}
	if r.Len() != 3 {
		t.Fatal("len")
	}
}

func TestAmbiguousAliasDoesNotLink(t *testing.T) {
	r := New([]Entity{{ID: "a", Name: "A", Type: "brand"}, {ID: "b", Name: "B", Type: "brand"}},
		[]Alias{{Alias: "shared", EntityID: "a"}, {Alias: "shared", EntityID: "b"}}, nil)
	if got := r.Link("", "shared"); got != "" {
		t.Fatalf("ambiguous alias linked to %s", got)
	}
	if got := r.Find("shared"); len(got) != 2 {
		t.Fatalf("find should report both candidates: %+v", got)
	}
}

func TestLoadSeed(t *testing.T) {
	s, err := LoadSeed("")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Entities) < 30 {
		t.Fatalf("seed has %d entities", len(s.Entities))
	}
	var ents []Entity
	var aliases []Alias
	for _, e := range s.Entities {
		ents = append(ents, Entity{ID: e.ID, Name: e.Name, NameAR: e.NameAR, Type: e.Type})
		for _, a := range e.Aliases {
			aliases = append(aliases, Alias{Alias: a, EntityID: e.ID})
		}
	}
	r := New(ents, aliases, nil)
	if m := r.Find("زين"); len(m) != 0 {
		t.Fatalf("the seed must not alias the common word زين: %+v", m)
	}
	if m := r.Find("رصيدي بكورك خلص"); len(m) != 1 || m[0].EntityID != "korek" {
		t.Fatalf("korek: %+v", m)
	}
}

func TestAmbiguousAliases(t *testing.T) {
	r := New([]Entity{
		{ID: "pos-iraq-pm", Name: "Prime Minister of Iraq", NameAR: "رئيس مجلس الوزراء", Type: "position", Tracked: true},
		{ID: "asiacell", Name: "Asiacell", Type: "brand", Tracked: true},
	}, []Alias{
		{Alias: "رئيس الوزراء العراقي", EntityID: "pos-iraq-pm"},
		{Alias: "رئيس الوزراء", EntityID: "pos-iraq-pm", Ambiguous: true},
		{Alias: "رئيس مجلس الوزراء", EntityID: "pos-iraq-pm", Ambiguous: true},
	}, nil)
	// A generic title: a hint marked ambiguous.
	m := r.Find("رئيس الوزراء البريطاني يزور بغداد")
	if len(m) != 1 || m[0].EntityID != "pos-iraq-pm" || !m[0].Ambiguous {
		t.Fatalf("generic title: %+v", m)
	}
	// The qualified form settles it, even next to the generic one.
	m = r.Find("رئيس الوزراء العراقي قال ... ورئيس الوزراء وعد")
	if len(m) != 1 || m[0].Ambiguous {
		t.Fatalf("qualified title: %+v", m)
	}
	// The Arabic name itself is generic here: no link by name alone.
	for _, n := range []string{"رئيس الوزراء", "رئيس مجلس الوزراء", "ورئيس الوزراء"} {
		if id := r.Link("", n); id != "" {
			t.Errorf("Link(%q) = %q, want no link", n, id)
		}
		if !r.Ambiguous(n) {
			t.Errorf("Ambiguous(%q) = false", n)
		}
	}
	// The model's id (chosen from the context) still links, and
	// unambiguous names link by name.
	if r.Link("pos-iraq-pm", "رئيس الوزراء") != "pos-iraq-pm" || r.Link("", "رئيس الوزراء العراقي") != "pos-iraq-pm" ||
		r.Link("", "Prime Minister of Iraq") != "pos-iraq-pm" || r.Ambiguous("Asiacell") || r.Ambiguous("unknown") {
		t.Fatal("unambiguous linking broken")
	}
}

// Known: a whole name that is an unambiguous alias of a public entity. A
// private person, a generic title, an unknown name or a longer name that
// only contains an alias are not.
func TestKnown(t *testing.T) {
	r := New([]Entity{
		{ID: "examplebrand", Name: "Example Brand", Type: "brand"},
		{ID: "someone", Name: "Private Person", Type: "person_private"},
		{ID: "pm", Name: "Prime Minister of Iraq", Type: "position"},
	}, []Alias{{Alias: "رئيس الوزراء", EntityID: "pm", Ambiguous: true}}, nil)
	for name, want := range map[string]bool{
		"Example Brand": true, "example brand": true, "Private Person": false, "رئيس الوزراء": false,
		"Sara Ahmed": false, "Ali Example Brand": false, "": false,
	} {
		if r.Known(name) != want {
			t.Errorf("Known(%q) = %v", name, !want)
		}
	}
	var none *Registry
	if none.Known("Example Brand") {
		t.Fatal("nil registry knows a name")
	}
}
