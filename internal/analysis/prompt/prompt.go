// Package prompt builds the versioned analysis prompts, the JSON schemas the
// providers must answer with, and validates their answers. The evaluation
// harness uses the same code, so it measures what production runs.
package prompt

import (
	"encoding/json"
	"strings"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/taxonomy"
)

// Versions recorded with every result. Bump PromptVersion on any change to
// the instructions and SchemaVersion on any change to the output schema.
const (
	PromptVersion = "p1"
	SchemaVersion = "s1"
)

// Allowed values of the output schema.
var (
	Langs       = []string{"ar", "ckb", "kmr", "en", "tr", "fa", "other", "none"}
	Dialects    = []string{"iraqi", "gulf", "levantine", "egyptian", "msa", "other", "none"}
	Scripts     = []string{"arabic", "latin", "mixed", "none"}
	Sentiments  = []string{"positive", "negative", "neutral", "mixed"}
	EntityTypes = []string{"brand", "product", "organization", "person_public", "person_private", "place", "event", "media_outlet", "other"}
	Intents     = []string{"complaint", "praise", "question", "request", "suggestion", "purchase_intent", "churn_threat", "recommendation", "information", "giveaway_entry", "greeting_religious", "tagging", "spam_ad", "other"}
	Emotions    = []string{"anger", "joy", "sadness", "fear", "surprise", "disgust", "trust", "anticipation", "none"}
	Toxicities  = []string{"none", "mild", "severe"}
	Aspects     = []string{"price", "quality", "network", "internet", "customer_service", "delivery", "billing", "app", "staff", "availability", "offers", "reliability", "safety", "management", "general", "other"}
)

const textInstructions = `You analyse Iraqi social media content for brand and media monitoring. Each request holds an optional thread context (the post that comments reply to, and the page that published it) and a list of items. Analyse every item and return exactly one result per item, with the same "i". The context only explains the items: do not analyse it unless it is itself listed as an item.

LANGUAGE
- Content is mostly Iraqi Arabic dialect (comments) and Modern Standard Arabic (news posts), also Arabizi (Arabic written in Latin letters and digits such as 3, 7, 5, 2), English, Kurdish and mixtures.
- lang: ar (any Arabic, including Arabizi), ckb (Kurdish Sorani, Arabic script with letters like ڕ ڵ ۆ ێ ە), kmr (Kurdish Kurmanji, Latin script), en, tr, fa, other; none when there are no words (emoji only).
- dialect (for ar): iraqi, gulf, levantine, egyptian, msa, other; none when not Arabic or too short to tell.
- script: arabic, latin, mixed, none. code_switch: true when the item mixes languages.

SENTIMENT
- The author's own evaluative stance, read in the context of the thread, not the tone of the news being commented on.
- positive, negative, neutral, or mixed (clear positive and clear negative parts). Thanks and blessings addressed to the page or brand ("الله يحفظكم", "عاشت ايدكم") are positive. Condolence and prayer formulas ("الله يرحمه", "انا لله") are neutral, with emotion sadness. Plain questions and tags are neutral.
- Sarcasm is common ("خوش شبكة 👏😂" under an outage post): label the intended meaning and set sarcasm true.
- score: -1 (very negative) to 1 (very positive). confidence: 0 to 1, how sure you are of the label.

ENTITIES
- List brands, products, organisations, public persons (politicians, officials, clerics, celebrities, journalists, athletes), places, events and media outlets that the item names or clearly refers to. On a brand's page, "your network", "شبكتكم", "انتم" refer to the page's brand.
- For each: name as written (or the canonical name from a hint), type, id, sentiment toward that entity with score, and aspect (what the sentiment is about).
- Hints list registry entities found in the text or the page name: when an entity is a hinted one, copy its id exactly; otherwise id is "".
- Private persons (friends tagged, ordinary people named): never list them. Count them in ppl.
- Do not identify people from images or guess identities.

TOPICS AND SIGNALS
- topics: up to 3 ids from the taxonomy below, most specific first; "other" if none fits. keywords: up to 5 short keywords in the item's language.
- intent: complaint, praise, question, request, suggestion, purchase_intent, churn_threat (says they will leave), recommendation, information (states facts or news), giveaway_entry (enters a contest), greeting_religious, tagging (only tags people), spam_ad, other.
- emotion: anger, joy, sadness, fear, surprise, disgust, trust, anticipation, none.
- tox: none, mild (insults, vulgarity), severe (hate speech, threats, incitement); hate, threat, harass flags.
- spam: advertising, scams, unrelated promotions, selling numbers or followers.
- urgent: the item alleges danger, an outage affecting many, fraud, a health or food-safety problem, or an accident: something a brand or authority must see quickly.

OUTPUT
Answer only with the JSON object the schema requires. Never translate, quote or explain the items.

TAXONOMY (id: English / Arabic)
`

const mediaInstructions = `You analyse images (or key frames of a video, in order, with its speech transcript) published on Iraqi social media, for brand and media monitoring. The post text, when given, is context.

- logos: brands visible as logos, signs or product packaging; name, id (copy a hint's id when it matches, else ""), confidence 0 to 1.
- ocr: text visible in the image, verbatim in its original language, at most 500 characters ("" if none).
- desc: one English sentence describing what is shown. labels: up to 8 English nouns for objects and scene.
- people: how many people are visible. Never identify anyone from their face; name a person only if the name is written in the image or spoken in the transcript.
- sent, score, conf: the sentiment of the message the image or video conveys (positive, negative, neutral, mixed; -1 to 1; 0 to 1).
- ents: brands, organisations, public persons, places and events shown, written or spoken, with sentiment toward each (same rules as for text; never private persons). topics: up to 3 taxonomy ids.

Answer only with the JSON object the schema requires.

TAXONOMY (id: English / Arabic)
`

// TextSystem is the static system prompt of the text routes. It is
// byte-identical across requests so providers can cache it.
func TextSystem(tax *taxonomy.Taxonomy) string {
	return textInstructions + tax.Prompt()
}

// MediaSystem is the static system prompt of the image and video routes.
func MediaSystem(tax *taxonomy.Taxonomy) string {
	return mediaInstructions + tax.Prompt()
}

// Hint is a registry entity found in an item's text (or the page name).
type Hint struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Context is the thread an item group belongs to.
type Context struct {
	// Page is the source's display name, PageHints its registry matches.
	Page      string
	PageHints []Hint
	// Post is the thread's post text (truncated), "" for the post route.
	Post string
}

// ItemInput is one item of a request.
type ItemInput struct {
	I    int    `json:"i"`
	Kind string `json:"kind"`
	Text string `json:"text"`
	// ReplyTo is the index of the parent item in the same request, or -1.
	ReplyTo int `json:"-"`
	// ParentText is the parent comment's text when it is not in the request.
	ParentText string `json:"-"`
	Hints      []Hint `json:"-"`
}

type itemJSON struct {
	I          int    `json:"i"`
	Kind       string `json:"kind"`
	Text       string `json:"text"`
	ReplyTo    *int   `json:"reply_to,omitempty"`
	ParentText string `json:"parent_text,omitempty"`
	Hints      []Hint `json:"hints,omitempty"`
}

// TextUser renders the user message of a text request.
func TextUser(c Context, items []ItemInput) string {
	var b strings.Builder
	if c.Page != "" || c.Post != "" {
		b.WriteString("<context>\n")
		if c.Page != "" {
			b.WriteString("page: ")
			b.WriteString(c.Page)
			b.WriteByte('\n')
			if len(c.PageHints) > 0 {
				h, _ := json.Marshal(c.PageHints)
				b.WriteString("page_hints: ")
				b.Write(h)
				b.WriteByte('\n')
			}
		}
		if c.Post != "" {
			b.WriteString("post: ")
			b.WriteString(c.Post)
			b.WriteByte('\n')
		}
		b.WriteString("</context>\n")
	}
	out := make([]itemJSON, len(items))
	for k, it := range items {
		j := itemJSON{I: it.I, Kind: it.Kind, Text: it.Text, ParentText: it.ParentText, Hints: it.Hints}
		if it.ReplyTo >= 0 && it.ReplyTo != it.I {
			r := it.ReplyTo
			j.ReplyTo = &r
		}
		out[k] = j
	}
	enc, _ := json.Marshal(out)
	b.WriteString("<items>\n")
	b.Write(enc)
	b.WriteString("\n</items>")
	return b.String()
}

// MediaUser renders the text part of an image or video request.
func MediaUser(postText string, hints []Hint, transcript string, frames int) string {
	var b strings.Builder
	if postText != "" {
		b.WriteString("post: ")
		b.WriteString(postText)
		b.WriteByte('\n')
	}
	if len(hints) > 0 {
		h, _ := json.Marshal(hints)
		b.WriteString("hints: ")
		b.Write(h)
		b.WriteByte('\n')
	}
	if frames > 1 {
		b.WriteString("The images are key frames of one video, in order.\n")
	}
	if transcript != "" {
		b.WriteString("transcript: ")
		b.WriteString(transcript)
		b.WriteByte('\n')
	}
	b.WriteString("Analyse the attached image")
	if frames > 1 {
		b.WriteString("s")
	}
	b.WriteByte('.')
	return b.String()
}
