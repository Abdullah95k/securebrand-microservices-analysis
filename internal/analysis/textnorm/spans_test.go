package textnorm

import "testing"

func TestUTF16Span(t *testing.T) {
	s := "ab😀cd" // 😀 is two UTF-16 units and four bytes
	for _, c := range []struct {
		off, n     int
		ok         bool
		start, end int
	}{
		{0, 2, true, 0, 2}, {2, 2, true, 2, 6}, {4, 2, true, 6, 8}, {5, 1, true, 7, 8},
		{3, 1, false, 0, 0}, {2, 1, false, 0, 0}, {5, 2, false, 0, 0}, {-1, 1, false, 0, 0}, {0, 0, false, 0, 0},
	} {
		sp, ok := UTF16Span(s, c.off, c.n)
		if ok != c.ok || (ok && (sp.Start != c.start || sp.End != c.end)) {
			t.Errorf("UTF16Span(%d,%d) = %+v %v", c.off, c.n, sp, ok)
		}
	}
	// Arabic letters are one unit each, as runes.
	ar := "شكرا أحمد"
	if sp, ok := UTF16Span(ar, 5, 4); !ok || ar[sp.Start:sp.End] != "أحمد" {
		t.Fatalf("arabic span %+v %v", sp, ok)
	}
}

func TestMaskSpans(t *testing.T) {
	s := "Ali and Sara 😀 love Example Brand"
	ali := Span{0, 3}
	sara := Span{8, 12}
	brand := Span{len(s) - len("Example Brand"), len(s)}
	keep := func(name string) bool { return name == "Example Brand" }
	if got := MaskSpans(s, []Span{sara, ali, brand}, TagPlaceholder, keep); got != "@user and @user 😀 love Example Brand" {
		t.Fatalf("got %q", got)
	}
	if got := MaskSpans(s, nil, TagPlaceholder, nil); got != s {
		t.Fatalf("no spans changed the text: %q", got)
	}
	// Overlapping, inverted, out-of-range and mid-character spans are
	// ignored; the valid one is still masked.
	emoji := len("Ali and Sara ")
	bad := []Span{ali, {1, 5}, {5, 4}, {0, len(s) + 1}, {emoji + 1, emoji + 3}, {emoji + 1, emoji + 4}}
	if got := MaskSpans(s, bad, TagPlaceholder, nil); got != "@user and Sara 😀 love Example Brand" {
		t.Fatalf("bad spans: %q", got)
	}
}

func TestSpansInts(t *testing.T) {
	sp := []Span{{0, 3}, {8, 12}}
	got := IntsToSpans(SpansToInts(sp))
	if len(got) != 2 || got[0] != sp[0] || got[1] != sp[1] || SpansToInts(nil) != nil || len(IntsToSpans([]int32{1})) != 0 {
		t.Fatalf("round trip %+v", got)
	}
}

// The placeholder of a hidden tag is never a keyword.
func TestKeywordsSkipPlaceholder(t *testing.T) {
	for _, k := range Keywords(TagPlaceholder+" شوفي العرض الجديد "+TagPlaceholder, 10) {
		if k == "user" || k == TagPlaceholder {
			t.Fatalf("placeholder kept as keyword: %v", Keywords(TagPlaceholder+" شوفي العرض", 10))
		}
	}
}
