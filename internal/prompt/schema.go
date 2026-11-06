package prompt

import (
	"bytes"
	"encoding/json"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/taxonomy"
)

// The schemas use only what every structured-output implementation accepts
// (types, enums, required, additionalProperties: false); ranges and list
// lengths are enforced by the validation in this package.

func str() map[string]any               { return map[string]any{"type": "string"} }
func integer() map[string]any           { return map[string]any{"type": "integer"} }
func enum(vals []string) map[string]any { return map[string]any{"type": "string", "enum": vals} }
func array(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

func object(props map[string]any, order []string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           ordered{keys: order, m: props},
		"required":             order,
		"additionalProperties": false,
	}
}

// ordered keeps the declared property order in the JSON schema
// (encoding/json would sort a map's keys). Models write fields in schema
// order, so "i" comes first and every label before its score and
// confidence.
type ordered struct {
	keys []string
	m    map[string]any
}

func (o ordered) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(k)
		val, err := json.Marshal(o.m[k])
		if err != nil {
			return nil, err
		}
		b.Write(key)
		b.WriteByte(':')
		b.Write(val)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func entitySchema() map[string]any {
	return object(map[string]any{
		"name":   str(),
		"type":   enum(EntityTypes),
		"id":     str(),
		"sent":   enum(Sentiments),
		"score":  integer(),
		"aspect": enum(Aspects),
	}, []string{"name", "type", "id", "sent", "score", "aspect"})
}

// ItemFields are the per-item output fields, in schema order. The format
// is compact because output tokens are most of the cost: one language
// code instead of language and dialect, the rare yes/no signals as a list
// of flags (usually empty), scores as whole numbers, and no script (the
// service detects it). Keywords ("kw") are asked only on the post route;
// comments get them from their text.
// New-topic proposals ("new_topics") follow the topics and are usually [].
var ItemFields = []string{"i", "lng", "sent", "score", "conf", "ents", "topics", "new_topics", "intent", "emotion", "tox", "flags", "ppl"}

// TextSchema is the output schema of the text routes: {"items": [...]}.
// keywords adds "kw" after "topics" (the post route).
func TextSchema(tax *taxonomy.Taxonomy, keywords bool) map[string]any {
	props := map[string]any{
		"i":      integer(),
		"lng":    enum(LangCodes),
		"sent":   enum(Sentiments),
		"score":  integer(),
		"conf":   integer(),
		"ents":   array(entitySchema()),
		"topics": array(enum(tax.IDs())),
		"new_topics": array(object(map[string]any{"en": str(), "ar": str(), "parent": enum(tax.ParentIDs())},
			[]string{"en", "ar", "parent"})),
		"intent":  enum(Intents),
		"emotion": enum(Emotions),
		"tox":     enum(Toxicities),
		"flags":   array(enum(Flags)),
		"ppl":     integer(),
	}
	order := ItemFields
	if keywords {
		props["kw"] = array(str())
		order = make([]string, 0, len(ItemFields)+1)
		for _, f := range ItemFields {
			order = append(order, f)
			if f == "topics" {
				order = append(order, "kw")
			}
		}
	}
	return object(map[string]any{"items": array(object(props, order))}, []string{"items"})
}

// MediaSchema is the output schema of the image and video routes.
func MediaSchema(tax *taxonomy.Taxonomy) map[string]any {
	logo := object(map[string]any{"name": str(), "id": str(), "conf": integer()}, []string{"name", "id", "conf"})
	return object(map[string]any{
		"logos":  array(logo),
		"ocr":    str(),
		"desc":   str(),
		"labels": array(str()),
		"people": integer(),
		"sent":   enum(Sentiments),
		"score":  integer(),
		"conf":   integer(),
		"ents":   array(entitySchema()),
		"topics": array(enum(tax.IDs())),
		"flags":  array(enum(MediaFlags)),
	}, []string{"logos", "ocr", "desc", "labels", "people", "sent", "score", "conf", "ents", "topics", "flags"})
}
