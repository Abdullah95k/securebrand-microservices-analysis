package textnorm

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// TagPlaceholder stands for a tagged account whose name is hidden before
// text leaves the service.
const TagPlaceholder = "@user"

// UTF16Span converts a range counted in UTF-16 code units (as Facebook's
// API, and most platform APIs, count them: an emoji outside the basic
// plane is two) to a byte span of s. A range that does not start and end
// on a character boundary of s, or reaches past its end, is rejected
// rather than guessed.
func UTF16Span(s string, off, n int) (Span, bool) {
	if off < 0 || n <= 0 {
		return Span{}, false
	}
	start, end, u := -1, -1, 0
	for b, r := range s {
		if u == off {
			start = b
		}
		if u == off+n {
			end = b
			break
		}
		if r >= 0x10000 {
			u += 2
		} else {
			u++
		}
	}
	if end < 0 && u == off+n {
		end = len(s)
	}
	if start < 0 || end < 0 {
		return Span{}, false
	}
	return Span{Start: start, End: end}, true
}

// CodePointSpan converts a span counted in Unicode code points to bytes of
// s; false when it falls outside s.
func CodePointSpan(s string, off, n int) (Span, bool) {
	if off < 0 || n <= 0 {
		return Span{}, false
	}
	start, end, i := -1, -1, 0
	for b := range s {
		if i == off {
			start = b
		}
		if i == off+n {
			end = b
			break
		}
		i++
	}
	if end < 0 && i == off+n {
		end = len(s)
	}
	if start < 0 || end < 0 {
		return Span{}, false
	}
	return Span{Start: start, End: end}, true
}

// ByteSpan checks a span counted in bytes of s: false when it falls
// outside s or cuts a character.
func ByteSpan(s string, off, n int) (Span, bool) {
	if off < 0 || n <= 0 || off+n > len(s) || !utf8.RuneStart(s[off]) || (off+n < len(s) && !utf8.RuneStart(s[off+n])) {
		return Span{}, false
	}
	return Span{Start: off, End: off + n}, true
}

// MaskSpans replaces each span of s with placeholder, except those whose
// text keep reports true for (keep may be nil). Spans that overlap an
// earlier one, fall outside s or cut a character are ignored, so a bad
// span can never expose or corrupt the rest of the text.
func MaskSpans(s string, spans []Span, placeholder string, keep func(string) bool) string {
	if len(spans) == 0 {
		return s
	}
	ss := append([]Span(nil), spans...)
	sort.Slice(ss, func(i, j int) bool { return ss[i].Start < ss[j].Start })
	var b strings.Builder
	last := 0
	for _, sp := range ss {
		if sp.Start < last || sp.End <= sp.Start || sp.End > len(s) ||
			!utf8.RuneStart(s[sp.Start]) || (sp.End < len(s) && !utf8.RuneStart(s[sp.End])) {
			continue
		}
		if keep != nil && keep(s[sp.Start:sp.End]) {
			continue
		}
		b.WriteString(s[last:sp.Start])
		b.WriteString(placeholder)
		last = sp.End
	}
	if last == 0 && b.Len() == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// SpansToInts flattens spans for storage (start, end, start, end, ...).
func SpansToInts(spans []Span) []int32 {
	if len(spans) == 0 {
		return nil
	}
	out := make([]int32, 0, 2*len(spans))
	for _, sp := range spans {
		out = append(out, int32(sp.Start), int32(sp.End))
	}
	return out
}

// IntsToSpans is the inverse of SpansToInts; a trailing odd value is
// ignored.
func IntsToSpans(xs []int32) []Span {
	var out []Span
	for i := 0; i+1 < len(xs); i += 2 {
		out = append(out, Span{Start: int(xs[i]), End: int(xs[i+1])})
	}
	return out
}
