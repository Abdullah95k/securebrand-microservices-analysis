package prompt

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// EntityCurationSystem instructs the model that grows the entity registry
// (brands, organisations, public figures, positions, places...) from the
// names the analysis model found.
const EntityCurationSystem = `You maintain the entity registry of a service that monitors Iraqi social media for brands and media analysts. The analysis model found names that the registry does not know (candidates). For each candidate you get its type, how many items named it, up to two example texts, and the most similar registry entities. Decide:

- merge, with "into" set to a registry entity id, when it is the same entity: another spelling, a transliteration, Arabizi, an abbreviation, a nickname ("اسيا سيل", "asia cell" into asiacell).
- approve when it is a real, specific entity worth tracking: a brand, product, organisation, public figure (politician, official, cleric, celebrity, journalist, athlete, influencer or public account), position (an office such as a minister), place, event or media outlet. Give an id (lower-case ASCII words joined by hyphens, from the English name), the English name, the Arabic name, the type, and public_figure (true for persons).
- reject when it is a private person (an ordinary individual named or tagged), a generic word or bare title that could mean many entities ("الشركة", "الوزير", "رئيس الوزراء" alone), a name you cannot identify, or not an entity.

When unsure whether a person is public, reject: private persons must never be stored. Answer only with the JSON object the schema requires, one decision per candidate.`

// EntityCandidate is a candidate entity shown to the curation model.
type EntityCandidate struct {
	Key      string   `json:"key"`
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Seen     int      `json:"items"`
	Examples []string `json:"examples,omitempty"`
	Similar  []string `json:"similar_entities,omitempty"`
}

// EntityCurationUser renders the candidates.
func EntityCurationUser(cands []EntityCandidate) string {
	enc, _ := json.Marshal(cands)
	return "<candidates>\n" + string(enc) + "\n</candidates>"
}

// curatedTypes are the types a curated entity may have (never a private
// person).
func curatedTypes() []string {
	var out []string
	for _, t := range EntityTypes {
		if t != "person_private" {
			out = append(out, t)
		}
	}
	return out
}

// EntityCurationSchema is the answer schema: {"decisions": [...]}.
func EntityCurationSchema() map[string]any {
	d := object(map[string]any{
		"key":           str(),
		"action":        enum([]string{"approve", "merge", "reject"}),
		"into":          str(),
		"id":            str(),
		"name":          str(),
		"name_ar":       str(),
		"type":          enum(curatedTypes()),
		"public_figure": map[string]any{"type": "boolean"},
	}, []string{"key", "action", "into", "id", "name", "name_ar", "type", "public_figure"})
	return object(map[string]any{"decisions": array(d)}, []string{"decisions"})
}

// EntityDecision is one validated decision.
type EntityDecision struct {
	Key          string `json:"key"`
	Action       string `json:"action"`
	Into         string `json:"into"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	NameAR       string `json:"name_ar"`
	Type         string `json:"type"`
	PublicFigure bool   `json:"public_figure"`
}

var entityID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

// ParseEntityCuration validates the decisions: known candidates only, a
// merge into an existing entity (exists reports it), an approval with a
// valid id, a name and an allowed type.
func ParseEntityCuration(raw string, cands []EntityCandidate, exists func(id string) bool) ([]EntityDecision, error) {
	js, err := ExtractJSON(raw)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Decisions []EntityDecision `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(js), &doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidJSON, err)
	}
	known := map[string]bool{}
	for _, c := range cands {
		known[c.Key] = true
	}
	var out []EntityDecision
	done := map[string]bool{}
	for _, d := range doc.Decisions {
		if !known[d.Key] || done[d.Key] {
			continue
		}
		switch d.Action {
		case "merge":
			if !exists(d.Into) {
				continue // left for the next round
			}
		case "approve":
			d.ID = strings.TrimSpace(strings.ToLower(d.ID))
			d.Name, d.NameAR = cut(strings.TrimSpace(d.Name), maxNameRunes), cut(strings.TrimSpace(d.NameAR), maxNameRunes)
			if !entityID.MatchString(d.ID) || d.Name == "" || !oneOf(d.Type, curatedTypes()) {
				continue
			}
			if exists(d.ID) {
				// The id names a known entity: the same one, a spelling.
				d = EntityDecision{Key: d.Key, Action: "merge", Into: d.ID}
			}
		case "reject":
		default:
			continue
		}
		done[d.Key] = true
		out = append(out, d)
	}
	return out, nil
}
