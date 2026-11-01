package prompt

import "github.com/abdullah95k/securebrand-microservices/internal/analysis/taxonomy"

// The schemas use only what every structured-output implementation accepts
// (types, enums, required, additionalProperties: false); ranges and list
// lengths are enforced by the validation in this package.

func str() map[string]any               { return map[string]any{"type": "string"} }
func num() map[string]any               { return map[string]any{"type": "number"} }
func integer() map[string]any           { return map[string]any{"type": "integer"} }
func boolean() map[string]any           { return map[string]any{"type": "boolean"} }
func enum(vals []string) map[string]any { return map[string]any{"type": "string", "enum": vals} }
func array(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

func object(props map[string]any, order []string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             order,
		"additionalProperties": false,
	}
}

func entitySchema() map[string]any {
	return object(map[string]any{
		"name":   str(),
		"type":   enum(EntityTypes),
		"id":     str(),
		"sent":   enum(Sentiments),
		"score":  num(),
		"aspect": enum(Aspects),
	}, []string{"name", "type", "id", "sent", "score", "aspect"})
}

// ItemFields are the per-item output fields, in schema order.
var ItemFields = []string{"i", "lang", "dialect", "script", "cs", "sent", "score", "conf", "ents", "topics", "kw",
	"intent", "emotion", "tox", "hate", "threat", "harass", "spam", "sarcasm", "urgent", "ppl"}

// TextSchema is the output schema of the text routes: {"items": [...]}.
func TextSchema(tax *taxonomy.Taxonomy) map[string]any {
	item := object(map[string]any{
		"i":       integer(),
		"lang":    enum(Langs),
		"dialect": enum(Dialects),
		"script":  enum(Scripts),
		"cs":      boolean(),
		"sent":    enum(Sentiments),
		"score":   num(),
		"conf":    num(),
		"ents":    array(entitySchema()),
		"topics":  array(enum(tax.IDs())),
		"kw":      array(str()),
		"intent":  enum(Intents),
		"emotion": enum(Emotions),
		"tox":     enum(Toxicities),
		"hate":    boolean(),
		"threat":  boolean(),
		"harass":  boolean(),
		"spam":    boolean(),
		"sarcasm": boolean(),
		"urgent":  boolean(),
		"ppl":     integer(),
	}, ItemFields)
	return object(map[string]any{"items": array(item)}, []string{"items"})
}

// MediaSchema is the output schema of the image and video routes.
func MediaSchema(tax *taxonomy.Taxonomy) map[string]any {
	logo := object(map[string]any{"name": str(), "id": str(), "conf": num()}, []string{"name", "id", "conf"})
	return object(map[string]any{
		"logos":  array(logo),
		"ocr":    str(),
		"desc":   str(),
		"labels": array(str()),
		"people": integer(),
		"sent":   enum(Sentiments),
		"score":  num(),
		"conf":   num(),
		"ents":   array(entitySchema()),
		"topics": array(enum(tax.IDs())),
	}, []string{"logos", "ocr", "desc", "labels", "people", "sent", "score", "conf", "ents", "topics"})
}
