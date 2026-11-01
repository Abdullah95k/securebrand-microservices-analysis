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
	PromptVersion = "p5"
	SchemaVersion = "s5"
)

// Allowed values of the output schema.
var (
	Langs       = []string{"ar", "ckb", "kmr", "en", "tr", "fa", "other", "none"}
	Dialects    = []string{"iraqi", "gulf", "levantine", "egyptian", "msa", "other", "none"}
	Scripts     = []string{"arabic", "latin", "mixed", "none"}
	Sentiments  = []string{"positive", "negative", "neutral", "mixed"}
	EntityTypes = []string{"brand", "product", "organization", "person_public", "person_private", "position", "place", "event", "media_outlet", "other"}
	Intents     = []string{"complaint", "praise", "question", "request", "suggestion", "purchase_intent", "churn_threat", "recommendation", "information", "giveaway_entry", "greeting_religious", "tagging", "spam_ad", "other"}
	Emotions    = []string{"anger", "joy", "sadness", "fear", "surprise", "disgust", "trust", "anticipation", "none"}
	Toxicities  = []string{"none", "mild", "severe"}
	// LangCodes is the model's one-field answer for language and dialect;
	// LangOf maps it back.
	LangCodes = []string{"ar-iq", "ar-gulf", "ar-lev", "ar-eg", "ar-msa", "ar-other", "ar", "ckb", "kmr", "en", "tr", "fa", "other", "none"}
	// SignalFlags, AdFlags and LegalFlags are the yes/no signals the model
	// lists only when they apply (usually none, so they cost nothing).
	SignalFlags = []string{"cs", "sarcasm", "spam", "hate", "threat", "harass", "urgent"}
	// AdFlags: affiliate marketing (commission links or codes), a brand or
	// business promoting itself, paid promotion of another brand
	// (disclosed), promotion that looks paid but is not disclosed, and
	// private sales (classifieds).
	AdFlags = []string{"ad_affiliate", "ad_own", "ad_paid", "ad_hidden", "ad_sale"}
	// LegalFlags are possible legal or platform-policy breaches the model
	// lists, flagged for human review (never a legal finding).
	LegalFlags = []string{"incitement", "terrorism", "child_safety", "sexual_content", "graphic_violence", "self_harm", "drugs",
		"weapons", "gambling", "regulated_goods", "fraud_scam", "unlicensed_finance", "counterfeit", "privacy", "defamation",
		"misinformation", "extortion", "religious_offense", "other_illegal"}
	// LegalCategories are every stored breach category: hate speech and
	// threats come from the hate and threat signals, the rest from
	// LegalFlags.
	LegalCategories = concat([]string{"hate_speech", "threat"}, LegalFlags)
	// Flags is every flag of the text routes; MediaFlags those of images
	// and videos.
	Flags      = concat(SignalFlags, AdFlags, LegalFlags)
	MediaFlags = concat(AdFlags, LegalFlags)
	Aspects    = []string{"price", "quality", "network", "internet", "customer_service", "delivery", "billing", "app", "staff", "availability", "offers", "reliability", "safety", "management", "general", "other"}
)

func concat(lists ...[]string) []string {
	var out []string
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// LangOf maps a language code to the stored language and dialect.
func LangOf(code string) (lang, dialect string, ok bool) {
	switch code {
	case "ar-iq":
		return "ar", "iraqi", true
	case "ar-gulf":
		return "ar", "gulf", true
	case "ar-lev":
		return "ar", "levantine", true
	case "ar-eg":
		return "ar", "egyptian", true
	case "ar-msa":
		return "ar", "msa", true
	case "ar-other":
		return "ar", "other", true
	case "ar", "ckb", "kmr", "en", "tr", "fa", "other", "none":
		return code, "none", true
	}
	return "other", "none", false
}

const textInstructions = `You analyse Iraqi social media content for brand and media monitoring. Each request holds an optional thread context (the post that comments reply to, and the page that published it) and a list of items. Analyse every item and return exactly one result per item, with the same "i". The context only explains the items: do not analyse it unless it is itself listed as an item.

LANGUAGE
- Content is mostly Iraqi Arabic dialect (comments) and Modern Standard Arabic (news posts), also Arabizi (Arabic written in Latin letters and digits such as 3, 7, 5, 2), English, Kurdish and mixtures.
- lng, one code for language and dialect. Arabic, including Arabizi: ar-iq (Iraqi), ar-gulf, ar-lev (Levantine), ar-eg (Egyptian), ar-msa (Modern Standard), ar-other, or ar when too short to tell the dialect. Otherwise: ckb (Kurdish Sorani, Arabic script with letters like ڕ ڵ ۆ ێ ە), kmr (Kurdish Kurmanji, Latin script), en, tr, fa, other; none when there are no words (emoji only).

SENTIMENT
- The author's own evaluative stance, read in the context of the thread, not the tone of the news being commented on.
- positive, negative, neutral, or mixed (clear positive and clear negative parts). Thanks and blessings addressed to the page or brand ("الله يحفظكم", "عاشت ايدكم") are positive. Condolence and prayer formulas ("الله يرحمه", "انا لله") are neutral, with emotion sadness. Plain questions and tags are neutral.
- Sarcasm is common ("خوش شبكة 👏😂" under an outage post): label the intended meaning and add the sarcasm flag.
- score: a whole number from -100 (very negative) to 100 (very positive). conf: a whole number from 0 to 100, how sure you are of the label.

ENTITIES
- List brands, products, organisations, public persons (politicians, officials, clerics, celebrities, journalists, athletes), positions, places, events and media outlets that the item names or clearly refers to. On a brand's page, "your network", "شبكتكم", "انتم" refer to the page's brand.
- Positions are offices, independent of who holds them: "رئيس الوزراء", "رئيس الحكومة" and "the PM" are the position "Prime Minister of Iraq"; also ministers ("وزير الكهرباء"), governors, the Speaker, the President, heads of bodies. Type position, name in English as "<office> of <country or body>". When the item also names or clearly means the person holding the office, list the person too.
- For each: name as written (or the canonical name from a hint), type, id, sentiment toward that entity with its score (-100 to 100), and aspect (what the sentiment is about).
- Hints list registry entities found in the text or the page name: when an entity is a hinted one, copy its id exactly; otherwise id is "".
- A hint marked ambiguous matched only a generic name ("رئيس الوزراء", "وزير الصحة", "البنك المركزي"). Use its id only when the item clearly means that entity: Iraqi context, no other country named. "رئيس الوزراء البريطاني" or "وزير الصحة اللبناني" is another entity: list it without the id ("Prime Minister of the United Kingdom", type position).
- Private persons (friends tagged, ordinary people named): never list them. Count them in ppl.
- Do not identify people from images or guess identities.

TOPICS AND SIGNALS
- topics: every taxonomy id below that the item is about, said directly or only implied (slang, complaints, jokes): "الوطنية طافية من الصبح" and "امبيرات المولدة غالية" are about electricity supply even without the word electricity. Most items have one to three; at most 10; most specific first; "other" only if nothing fits and nothing is proposed. kw (only when the schema asks for it): up to 5 short keywords in the item's language.
- new_topics: only when the item is clearly about a theme that no listed topic covers: en (2 to 4 English words), ar (Arabic), parent (an area, or the listed topic it narrows: "Electricity cuts" goes under services.electricity). Usually []. Never propose a synonym or translation of a listed topic: use the listed one. Never propose names of people, companies, positions or places: list those as entities (they become topics automatically).
- intent: complaint, praise, question, request, suggestion, purchase_intent, churn_threat (says they will leave), recommendation, information (states facts or news), giveaway_entry (enters a contest), greeting_religious, tagging (only tags people), spam_ad, other.
- emotion: anger, joy, sadness, fear, surprise, disgust, trust, anticipation, none.
- tox: none, mild (insults, vulgarity), severe (hate speech, threats, incitement).
- flags: list only what applies, usually none ([]).
  Signals:
  - cs: the item mixes languages (code switching);
  - sarcasm: the intended meaning is the opposite of the literal one;
  - spam: advertising, scams, unrelated promotions, selling numbers or followers;
  - hate, threat, harass: hate speech, a threat, harassment of a person;
  - urgent: the item alleges danger, an outage affecting many, fraud, a health or food-safety problem, or an accident: something a brand or authority must see quickly.
  Advertising (mainly posts; for comments use spam), at most one:
  - ad_affiliate: affiliate marketing: promotes products for a commission through referral or affiliate links, discount or referral codes ("استخدم كودي", "الرابط بالبايو", Temu, Shein or AliExpress codes);
  - ad_own: a brand, shop or business promoting its own products, services or offers (on its page or elsewhere);
  - ad_paid: disclosed paid promotion of another brand ("sponsored", "إعلان", "برعاية", a paid partnership);
  - ad_hidden: reads like paid promotion of a brand but is not disclosed: an influencer or news page praising a product with prices, links or calls to buy;
  - ad_sale: a private person selling or buying something (classifieds: cars, phones, property).
  Possible legal or platform-policy breaches, for human review (hate speech and threats are already covered by the hate and threat signals). Flag when the item itself does it, not when it reports or condemns it:
  - incitement: calls for violence against anyone (a group, a person, institutions, security forces), for revenge or tribal attacks, or for hatred against a sect, ethnicity, religion, tribe or nationality;
  - terrorism: praise, support or propaganda for terrorist or armed extremist groups;
  - child_safety: sexualisation, abuse or exploitation of minors;
  - sexual_content: explicit sexual content, nudity, or offers of sexual services;
  - graphic_violence: gore, or celebrating injury or killing;
  - self_harm: encouraging suicide or self-harm;
  - drugs: selling, buying or promoting illegal drugs;
  - weapons: selling or buying weapons or ammunition;
  - gambling: promoting betting or gambling;
  - regulated_goods: selling alcohol, prescription medicines without prescription, or other restricted goods;
  - fraud_scam: scams, phishing, fake giveaways, fake jobs, pyramid schemes, impersonating a brand or official page;
  - unlicensed_finance: unlicensed currency trading, investment or loan offers promising returns;
  - counterfeit: fake branded goods, pirated content or accounts for sale;
  - privacy: publishing someone's phone number, address, ID, private photos or messages;
  - defamation: a specific accusation of a crime or serious wrongdoing against a named person (officials included) or company, stated as fact without evidence or a source;
  - misinformation: a claim presented as fact that is false or very likely false and can cause harm or panic: health and medicine, security incidents, disasters, elections, shortages, the dollar rate or prices, fabricated quotes or documents; flag only clear cases, not opinions or predictions;
  - extortion: blackmail, threats to publish private photos or information unless paid, sextortion;
  - religious_offense: insults, mockery, ridicule or attacks on a religion or sect, a sacred text, a prophet, imam or saint, a religious symbol or rite, or a religious figure as a religious figure; desecration; calls to abandon or attack a faith (punished by Iraqi law, and a cause of unrest). Always flag these, whoever the target;
  - other_illegal: clearly illegal in Iraq and not covered above: human trafficking, bribery offers, vote buying, selling official documents or exam questions, organ trade.
  Criticism, opinions, satire, complaints and news reporting are not breaches by themselves; flag them when they also do one of the above (criticism that states an unproven crime as fact is defamation; mocking a sacred figure is religious_offense; a complaint that calls for attacking a sect is incitement). Not breaches by themselves: peaceful political opinion; a respectful religious question, disagreement, or a person stating their own belief or doubt; criticism of a cleric's political actions (that is politics); a person's sexuality or gender identity.

OUTPUT
Answer only with the JSON object the schema requires. Never translate, quote or explain the items.

TAXONOMY (id: English / Arabic)
`

const mediaInstructions = `You analyse images (or key frames of a video, in order, with its speech transcript) published on Iraqi social media, for brand and media monitoring. The post text, when given, is context.

- logos: brands visible as logos, signs or product packaging; name, id (copy a hint's id when it matches, else ""), conf (0 to 100).
- ocr: text visible in the image, verbatim in its original language, at most 500 characters ("" if none).
- desc: one English sentence describing what is shown. labels: up to 8 English nouns for objects and scene.
- people: how many people are visible. Never identify anyone from their face; name a person only if the name is written in the image or spoken in the transcript.
- sent, score, conf: the sentiment of the message the image or video conveys (positive, negative, neutral, mixed; a whole number from -100 to 100; a whole number from 0 to 100).
- ents: brands, organisations, public persons, positions, places and events shown, written or spoken, with sentiment toward each (same rules as for text; never private persons). topics: every taxonomy id that applies (at most 10).
- flags, usually []: ad_affiliate (affiliate links or codes for a commission), ad_own (a business promoting itself), ad_paid (disclosed paid promotion of another brand), ad_hidden (looks like undisclosed paid promotion), ad_sale (a private sale), and possible legal or platform-policy breaches for human review: incitement, terrorism, child_safety, sexual_content, graphic_violence, self_harm, drugs, weapons, gambling, regulated_goods, fraud_scam, unlicensed_finance, counterfeit, privacy, defamation, misinformation (fabricated or doctored images, false claims as fact), extortion, religious_offense (insulting or mocking a religion, sacred figure or symbol), other_illegal (same meanings as for text; news images reporting such things are not breaches).

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
// Ambiguous: only a generic name matched; the model decides.
type Hint struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Ambiguous bool   `json:"ambiguous,omitempty"`
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
