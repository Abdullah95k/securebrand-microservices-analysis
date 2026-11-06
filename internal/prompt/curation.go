package prompt

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/taxonomy"
)

// CurationSystem instructs the model that keeps the topic list clean.
const CurationSystem = `You maintain the topic list of a service that monitors Iraqi social media (news pages and brand pages) for brands and media analysts. The analysis model proposed new topics (candidates) for items that no listed topic covered. Decide for every candidate:

- merge, with "into" set to a listed topic id or another candidate id, when it means the same as that topic: a synonym, a translation, a near-identical wording, a spelling variant ("power outages" into "Electricity cuts").
- reject when it is not a topic: a name of a person, company, brand, position or place (those are tracked as entities), a one-off detail of a single event, too vague ("issues", "news"), or not about Iraqi public or brand matters.
- approve when it is a reusable theme. Give a clean English name (2 to 4 words, title case), an Arabic name, and the right parent: an area, or the listed topic it narrows, so that asking for the broader topic also finds it ("Electricity cuts" under services.electricity).

Prefer merging over approving near-duplicates; the list must not contain two topics with the same meaning. Answer only with the JSON object the schema requires, one decision per candidate.`

// CurationCandidate is a candidate topic shown to the curation model.
type CurationCandidate struct {
	ID     string `json:"id"`
	EN     string `json:"en"`
	AR     string `json:"ar"`
	Parent string `json:"parent"`
	Seen   int    `json:"items"`
}

// CurationUser renders the topic list and the candidates.
func CurationUser(tax *taxonomy.Taxonomy, cands []CurationCandidate) string {
	var b strings.Builder
	b.WriteString("<topics>\n")
	b.WriteString(tax.Prompt())
	b.WriteString("</topics>\n<candidates>\n")
	enc, _ := json.Marshal(cands)
	b.Write(enc)
	b.WriteString("\n</candidates>")
	return b.String()
}

// CurationSchema is the answer schema: {"decisions": [...]}.
func CurationSchema(tax *taxonomy.Taxonomy) map[string]any {
	d := object(map[string]any{
		"id":     str(),
		"action": enum([]string{"approve", "merge", "reject"}),
		"into":   str(),
		"en":     str(),
		"ar":     str(),
		"parent": enum(tax.ParentIDs()),
	}, []string{"id", "action", "into", "en", "ar", "parent"})
	return object(map[string]any{"decisions": array(d)}, []string{"decisions"})
}

// CurationDecision is one validated decision.
type CurationDecision struct {
	ID, Action, Into, EN, AR, Parent string
}

// ParseCuration validates the decisions: only known candidates, a merge
// only into a listed topic or another candidate.
func ParseCuration(raw string, tax *taxonomy.Taxonomy, cands []CurationCandidate) ([]CurationDecision, error) {
	js, err := ExtractJSON(raw)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Decisions []CurationDecision `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(js), &doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidJSON, err)
	}
	known := map[string]bool{}
	for _, c := range cands {
		known[c.ID] = true
	}
	var out []CurationDecision
	done := map[string]bool{}
	for _, d := range doc.Decisions {
		if !known[d.ID] || done[d.ID] {
			continue
		}
		switch d.Action {
		case "merge":
			if d.Into == d.ID || (!tax.Valid(d.Into) && !known[d.Into]) {
				continue // left as a candidate for the next round
			}
		case "approve":
			d.EN, d.AR = cut(strings.TrimSpace(d.EN), maxTopicRunes), cut(strings.TrimSpace(d.AR), maxTopicRunes)
			if !oneOf(d.Parent, tax.ParentIDs()) {
				d.Parent = ""
			}
		case "reject":
		default:
			continue
		}
		done[d.ID] = true
		out = append(out, d)
	}
	return out, nil
}
