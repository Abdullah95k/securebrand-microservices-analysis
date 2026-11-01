package textnorm

import (
	"sort"
	"strings"
	"unicode"
)

// Script classifies the letters of a text: arabic (Arabic script, which
// includes Kurdish Sorani and Persian), latin (English, Kurdish Kurmanji,
// Arabizi), mixed (the minority script holds at least a fifth of the
// letters), or none (no letters: emoji, digits). Links and masked contact
// details do not count. It replaces asking the model, which costs output
// tokens and is no more accurate.
func Script(text string) string {
	text = urlRe.ReplaceAllString(text, " ")
	text = strings.NewReplacer("[phone]", " ", "[email]", " ").Replace(text)
	arabic, latin := 0, 0
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Arabic, r) && unicode.IsLetter(r):
			arabic++
		case unicode.Is(unicode.Latin, r):
			latin++
		}
	}
	total := arabic + latin
	switch {
	case total == 0:
		return "none"
	case min(arabic, latin)*5 >= total:
		return "mixed"
	case arabic > latin:
		return "arabic"
	}
	return "latin"
}

// stopwords are function words of Modern Standard Arabic, Iraqi dialect
// and English, in normalised form; they are never keywords. Religious and
// courtesy formulas are included because they repeat in most threads and
// say nothing about the topic.
var stopwords = func() map[string]bool {
	words := `
في من الى علي على عن مع هذا هذه هاي هذي هذول هاذا ذلك تلك هناك هنا اللي الذي التي الذين اللذين
ما ماكو اكو مو مش لا لم لن ليش شنو شلون شكد وين منو اشو اي اية ايه كل كلش بس هم هو هي انت انتي انتم
احنا نحن اني انا انه انها ان او ثم حتي حتى كان كانت يكون تكون صار صارت راح رح هسه هسا هم بعد قبل
عند عدنا عدكم الي اله الله والله بالله يا يارب ياريت لو اذا اذ كيف ليس غير بين ضد خلي خل شي شيء
عليه عليها عليكم عليهم منه منها منهم لهم لها له لك لكم بيه بيها بيهم بي بك ويا وياكم وياهم جدا
قد لقد كما مثل حيث يعني تم شكرا مشكور مشكورين عاشت ايدكم ايدك الف مبروك اللهم صل محمد ال سلام
السلام عليكم ورحمه وبركاته رحمه يرحمه يرحمها اللهم امين انشاءالله ان شاء ماشاءالله سبحان الحمد لله
the a an and or but of to in on at for with is are was were be been it this that these those i you
he she we they my your our their me him her them not no yes so if then than as by from do does did
have has had will would can could just very also too please pls ok okay lol
`
	m := map[string]bool{}
	for _, w := range strings.Fields(words) {
		m[Normalize(w)] = true
	}
	return m
}()

// Keywords extracts up to n keywords from a text, as written (diacritics
// and tatweel removed, Latin lower-cased), in order of appearance: words
// of at least three letters that are not function words, digits, links or
// masked contact details. A word and its form with a clitic ("الشبكة",
// "والشبكة") count once. Comments are short, so their content words are
// their keywords; asking the model for them costs about a fifth of its
// answer. Posts still get the model's keywords.
func Keywords(text string, n int) []string {
	if n <= 0 {
		return nil
	}
	text = urlRe.ReplaceAllString(MaskPII(text), " ")
	words := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !isDiacritic(r)
	})
	var out []string
	seen := map[string]bool{}
	for _, w := range words {
		norm := Normalize(w)
		if norm == "" || isStopword(norm) || len([]rune(norm)) < 3 || !hasLetter(norm) {
			continue
		}
		key := norm
		if s := StripClitic(norm); s != "" {
			key = s
		}
		if stopwords[key] || seen[key] || key == "phone" || key == "email" {
			continue
		}
		seen[key] = true
		out = append(out, display(w))
		if len(out) == n {
			break
		}
	}
	return out
}

// isStopword reports a function word, also with a one-letter conjunction
// or preposition attached ("وال", "وهذا").
func isStopword(norm string) bool {
	if stopwords[norm] {
		return true
	}
	for _, c := range []string{"و", "ب", "ل", "ف"} {
		if rest, ok := strings.CutPrefix(norm, c); ok && stopwords[rest] {
			return true
		}
	}
	return false
}

func hasLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// display keeps a word as written without diacritics and tatweel; Latin
// is lower-cased; runs of three or more identical letters become one, as
// in Normalize ("حلوووو" -> "حلو").
func display(w string) string {
	rs := make([]rune, 0, len(w))
	for _, r := range w {
		if !isDiacritic(r) {
			rs = append(rs, unicode.ToLower(r))
		}
	}
	var b strings.Builder
	for i := 0; i < len(rs); {
		j := i
		for j < len(rs) && rs[j] == rs[i] {
			j++
		}
		n := j - i
		if n >= 3 && unicode.IsLetter(rs[i]) {
			n = 1
		}
		for k := 0; k < n; k++ {
			b.WriteRune(rs[i])
		}
		i = j
	}
	return b.String()
}

// TopicKey is the duplicate key of a topic name: normalised, function
// words and clitics dropped, a Latin plural "s" dropped, words sorted.
// "Electricity cuts", "cuts of electricity" and "the electricity cut"
// share a key; so do "انقطاع الكهرباء" and "انقطاع كهرباء". Synonyms in
// other words ("power outage") are left to the curation step.
func TopicKey(name string) string {
	var toks []string
	seen := map[string]bool{}
	for _, t := range Tokens(Normalize(name)) {
		if isStopword(t) {
			continue
		}
		if s := StripClitic(t); s != "" {
			t = s
		}
		if latinWord(t) && len(t) > 3 && strings.HasSuffix(t, "s") && !strings.HasSuffix(t, "ss") {
			t = t[:len(t)-1]
		}
		if !seen[t] {
			seen[t] = true
			toks = append(toks, t)
		}
	}
	sort.Strings(toks)
	return strings.Join(toks, " ")
}

func latinWord(s string) bool {
	for _, r := range s {
		if !unicode.Is(unicode.Latin, r) {
			return false
		}
	}
	return s != ""
}

// Similarity is the Jaccard similarity of two words' character trigrams
// (padded): "electrical" and "electricity" score 0.5, unrelated words 0.
func Similarity(a, b string) float64 {
	ta, tb := trigrams(a), trigrams(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	inter := 0
	for g := range ta {
		if tb[g] {
			inter++
		}
	}
	return float64(inter) / float64(len(ta)+len(tb)-inter)
}

func trigrams(w string) map[string]bool {
	rs := []rune(" " + w + " ")
	out := map[string]bool{}
	for i := 0; i+3 <= len(rs); i++ {
		out[string(rs[i:i+3])] = true
	}
	return out
}
