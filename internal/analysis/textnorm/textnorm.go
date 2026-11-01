// Package textnorm normalises Arabic, Kurdish, Arabizi and English text for
// matching, decides whether a text has anything to analyse, masks contact
// details before text leaves the service, and computes the analysis input
// hash.
package textnorm

import (
	"crypto/sha256"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// arabicFold maps letter variants to one form: alef with hamza/madda/wasla
// to bare alef, alef maqsura to yaa, taa marbuta to haa, hamza seats to
// their letter, and Arabic-Indic / Persian digits to ASCII.
var arabicFold = map[rune]rune{
	'أ': 'ا', 'إ': 'ا', 'آ': 'ا', 'ٱ': 'ا',
	'ى': 'ي', 'ة': 'ه', 'ؤ': 'و', 'ئ': 'ي',
	'ک': 'ك', 'ی': 'ي', // Persian/Kurdish kaf and yeh
	'٠': '0', '١': '1', '٢': '2', '٣': '3', '٤': '4', '٥': '5', '٦': '6', '٧': '7', '٨': '8', '٩': '9',
	'۰': '0', '۱': '1', '۲': '2', '۳': '3', '۴': '4', '۵': '5', '۶': '6', '۷': '7', '۸': '8', '۹': '9',
}

// isDiacritic reports Arabic harakat, superscript alef and tatweel, which
// carry no meaning for matching.
func isDiacritic(r rune) bool {
	return (r >= 0x064B && r <= 0x065F) || r == 0x0670 || r == 0x0640
}

// Normalize folds text for matching: lower-case, diacritics and tatweel
// removed, letter variants unified, runs of three or more identical letters
// collapsed to one ("حلوووو" -> "حلو"; doubles such as "الله" or "good"
// stay), everything that is not a letter or digit turned into a single
// space.
func Normalize(s string) string {
	rs := make([]rune, 0, len(s))
	for _, r := range s {
		if isDiacritic(r) {
			continue
		}
		if f, ok := arabicFold[r]; ok {
			r = f
		}
		r = unicode.ToLower(r)
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			r = ' '
		}
		rs = append(rs, r)
	}
	var b strings.Builder
	b.Grow(len(s))
	space := true
	for i := 0; i < len(rs); {
		j := i
		for j < len(rs) && rs[j] == rs[i] {
			j++
		}
		r, n := rs[i], j-i
		switch {
		case r == ' ':
			if !space {
				b.WriteByte(' ')
			}
			space = true
		case n >= 3 && unicode.IsLetter(r):
			b.WriteRune(r)
			space = false
		default:
			for k := 0; k < n; k++ {
				b.WriteRune(r)
			}
			space = false
		}
		i = j
	}
	return strings.TrimSpace(b.String())
}

// arabicClitics are prefixes stripped from a token to find a brand name
// written with a conjunction, preposition or article attached, longest
// first.
var arabicClitics = []string{"وبال", "وال", "بال", "فال", "كال", "لل", "ال", "و", "ب", "ل", "ف"}

// Tokens splits normalised text into words.
func Tokens(norm string) []string {
	if norm == "" {
		return nil
	}
	return strings.Fields(norm)
}

// StripClitic returns the token without a leading Arabic clitic, or "" when
// none applies or too little would remain.
func StripClitic(tok string) string {
	for _, c := range arabicClitics {
		if strings.HasPrefix(tok, c) {
			rest := strings.TrimPrefix(tok, c)
			if len([]rune(rest)) >= 3 {
				return rest
			}
		}
	}
	return ""
}

var (
	urlRe   = regexp.MustCompile(`(?i)\b(?:https?://|www\.)\S+`)
	emailRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
	// Phone numbers: an optional + or 00, then 9 or more digits possibly
	// separated by spaces, dashes or brackets; Western, Arabic-Indic and
	// Persian digits.
	phoneRe = regexp.MustCompile(`(?:\+|00)?[0-9٠-٩۰-۹](?:[0-9٠-٩۰-۹ ()\-]{7,})[0-9٠-٩۰-۹]`)
)

// MaskPII replaces e-mail addresses and phone numbers with placeholders.
// It runs on every text before it is sent to a provider.
func MaskPII(s string) string {
	s = emailRe.ReplaceAllString(s, "[email]")
	return phoneRe.ReplaceAllStringFunc(s, func(m string) string {
		digits := 0
		for _, r := range m {
			if unicode.IsDigit(r) {
				digits++
			}
		}
		if digits < 9 {
			return m
		}
		return "[phone]"
	})
}

// Span is a byte range of a text (a tagged account, for example).
type Span struct{ Start, End int }

// Analyzable reports whether text has anything to analyse once the given
// spans (tagged accounts), URLs and whitespace are removed: at least one
// letter, digit or symbol such as an emoji. A comment that only tags friends
// or only holds a link is not analysed.
func Analyzable(text string, remove []Span) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	if len(remove) > 0 {
		sort.Slice(remove, func(i, j int) bool { return remove[i].Start < remove[j].Start })
		var b strings.Builder
		last := 0
		for _, sp := range remove {
			if sp.Start < last || sp.Start > len(text) {
				continue
			}
			end := min(sp.End, len(text))
			b.WriteString(text[last:sp.Start])
			b.WriteByte(' ')
			last = end
		}
		b.WriteString(text[last:])
		text = b.String()
	}
	text = urlRe.ReplaceAllString(text, " ")
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.So, r) || unicode.Is(unicode.Sk, r) {
			return true
		}
	}
	return false
}

// InputHash is the idempotency key of an analysis input: kind, normalised
// whitespace of the text, and the media ids. Unchanged re-scrapes give the
// same hash; an edit or a new attachment changes it.
func InputHash(kind, text string, mediaIDs []string) []byte {
	h := sha256.New()
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write([]byte(strings.Join(strings.Fields(text), " ")))
	h.Write([]byte{0})
	ids := append([]string(nil), mediaIDs...)
	sort.Strings(ids)
	h.Write([]byte(strings.Join(ids, ",")))
	return h.Sum(nil)
}

// Truncate cuts s to at most n runes, on a word boundary when one is near.
func Truncate(s string, n int) string {
	r := []rune(s)
	if n <= 0 || len(r) <= n {
		return s
	}
	cut := n
	for i := n; i > n-40 && i > 0; i-- {
		if unicode.IsSpace(r[i]) {
			cut = i
			break
		}
	}
	return strings.TrimSpace(string(r[:cut])) + " …"
}
