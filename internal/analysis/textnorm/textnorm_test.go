package textnorm

import (
	"bytes"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		"آسياسيل":            "اسياسيل",
		"أسيا سيل!!":         "اسيا سيل",
		"مُمْتَاز":           "ممتاز",
		"حلـــو":             "حلو",
		"حلوووووو":           "حلو",
		"الله":               "الله",
		"مدرسة":              "مدرسه",
		"مستشفى":             "مستشفي",
		"Asiacell IS GOOOOD": "asiacell is god",
		"good":               "good",
		"٠٧٧٠ ١٢٣":           "0770 123",
		"  \n ":              "",
		"زين، كورك؟":         "زين كورك",
	}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripClitic(t *testing.T) {
	cases := map[string]string{
		"واسياسيل":  "اسياسيل",
		"بالكهرباء": "كهرباء",
		"الكهرباء":  "كهرباء",
		"وبي":       "", // too short once stripped
		"كورك":      "",
		"لل":        "",
	}
	for in, want := range cases {
		if got := StripClitic(Normalize(in)); got != Normalize(want) {
			t.Errorf("StripClitic(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaskPII(t *testing.T) {
	cases := map[string]string{
		"اتصل 07701234567 هسه":       "اتصل [phone] هسه",
		"رقمي ٠٧٧٠ ١٢٣ ٤٥٦٧":         "رقمي [phone]",
		"+964 770 123 4567 call me":  "[phone] call me",
		"mail a.b@example.com now":   "mail [email] now",
		"السعر 25000 دينار سنة 2026": "السعر 25000 دينار سنة 2026",
		"(0770) 123-4567":            "([phone]",
		"no contact details at all":  "no contact details at all",
	}
	for in, want := range cases {
		if got := MaskPII(in); got != want {
			t.Errorf("MaskPII(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnalyzable(t *testing.T) {
	text := "Ali Hassan Sara Ahmed"
	if Analyzable(text, []Span{{0, 10}, {11, 21}}) {
		t.Error("tag-only comment is analyzable")
	}
	if !Analyzable("Ali Hassan شكرا", []Span{{0, 10}}) {
		t.Error("tag plus text is not analyzable")
	}
	if !Analyzable("😂😂", nil) {
		t.Error("emoji-only comment is not analyzable")
	}
	if Analyzable("https://example.com/x?y=1   ", nil) {
		t.Error("link-only comment is analyzable")
	}
	if Analyzable(" .. ،، ", nil) || Analyzable("", nil) {
		t.Error("punctuation is analyzable")
	}
	if !Analyzable("٣", nil) {
		t.Error("a digit is not analyzable")
	}
	// Out-of-range spans are ignored, not a panic.
	if !Analyzable("ok", []Span{{5, 9}}) {
		t.Error("out-of-range span removed the text")
	}
}

func TestInputHash(t *testing.T) {
	a := InputHash("comment", "hello   world", []string{"m2", "m1"})
	b := InputHash("comment", " hello world ", []string{"m1", "m2"})
	if !bytes.Equal(a, b) {
		t.Fatal("whitespace or media order changed the hash")
	}
	for _, other := range [][]byte{
		InputHash("post", "hello world", []string{"m1", "m2"}),
		InputHash("comment", "hello world!", []string{"m1", "m2"}),
		InputHash("comment", "hello world", []string{"m1"}),
	} {
		if bytes.Equal(a, other) {
			t.Fatal("different input, same hash")
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("short", 10); got != "short" {
		t.Fatal(got)
	}
	got := Truncate("one two three four five", 12)
	if got != "one two …" && got != "one two three …" {
		t.Fatalf("Truncate = %q", got)
	}
	if len([]rune(Truncate("كلمة كلمة كلمة كلمة", 9))) > 12 {
		t.Fatal("truncate too long")
	}
}

func TestScript(t *testing.T) {
	cases := map[string]string{
		"النت ضعيف":           "arabic",
		"شلونكم ڕۆژباش":       "arabic",
		"shlonak 7abibi":      "latin",
		"النت bad اليوم":      "mixed",
		"النت ضعيف جدا ok":    "arabic",
		"😂😂 123":              "none",
		"":                    "none",
		"Asiacell الشبكة زين": "mixed",
		"الشبكة [phone] https://example.com/abc": "arabic",
	}
	for in, want := range cases {
		if got := Script(in); got != want {
			t.Errorf("Script(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKeywords(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"والله الشبكة ضعيفة اليوم، والشبكة تقطع", 5, "الشبكة ضعيفة اليوم تقطع"},
		{"النتتتت غاااالي كلش 😡 https://x.com/a اتصل 07701234567", 5, "النت غالي اتصل"},
		{"اللهم صل على محمد وال محمد", 5, ""},
		{"تم", 5, ""},
		{"Asiacell internet is VERY slow!!", 5, "asiacell internet slow"},
		{"a b c ابو", 5, "ابو"},
		{"مشكلة كبيرة جديدة قديمة صعبة سهلة", 3, "مشكلة كبيرة جديدة"},
		{"عرض 2026 رصيد", 0, ""},
	}
	for _, c := range cases {
		if got := strings.Join(Keywords(c.in, c.n), " "); got != c.want {
			t.Errorf("Keywords(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
