// Package core holds the analysis service's domain types, shared by the
// adapters, the store, the pipeline and the evaluation harness.
package core

import (
	"time"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/textnorm"
)

// Kinds of content items.
const (
	KindPost    = "post"
	KindComment = "comment"
	KindReply   = "reply"
)

// Item statuses.
const (
	StatusVisible  = "visible"
	StatusDeleted  = "deleted"
	StatusRedacted = "redacted"
)

// Routes.
const (
	RouteComment    = "comment"
	RoutePost       = "post"
	RouteImage      = "image"
	RouteVideo      = "video"
	RouteEscalation = "escalation"
	RouteAudit      = "audit"
)

// Lanes.
const (
	LaneBatch    = "batch"
	LaneRealtime = "realtime"
)

// Queue priorities.
const (
	PriorityUrgent     = 0
	PriorityNormal     = 1
	PriorityBackground = 2
)

// Item is a canonical content item, decoded from content.item.v1 or from a
// collector's topics by an adapter.
type Item struct {
	ContentID      string
	Platform       string // facebook | instagram | tiktok | x | telegram | youtube
	Kind           string // post | comment | reply
	SourceID       string
	SourceName     string
	ThreadID       string
	ParentID       string
	AuthorKey      []byte
	AuthorIsSource bool
	CreatedAt      time.Time
	ObservedAt     time.Time
	Text           string
	LangHint       string
	Hashtags       []string
	MediaIDs       []string
	Status         string
	// RemoveSpans are byte ranges of the text that carry no content of
	// their own (tagged accounts), for the analyzable check.
	RemoveSpans []textnorm.Span
}

// Analyzable reports whether the item has text worth analysing.
func (it *Item) Analyzable() bool { return textnorm.Analyzable(it.Text, it.RemoveSpans) }

// InputHash is the item's analysis idempotency key.
func (it *Item) InputHash() []byte { return textnorm.InputHash(it.Kind, it.Text, it.MediaIDs) }

// MediaJob is a downloaded media file to analyse and acknowledge.
type MediaJob struct {
	MediaID        string
	OwnerContentID string
	Platform       string
	SourceID       string
	// Kind: photo | video | reel | thumbnail | comment_photo | comment_gif |
	// sticker | comment_video | cover | profile_pic | image | gif | audio.
	Kind        string
	Bucket      string
	Key         string
	Size        int64
	ContentType string
	DurationMS  int64
	AltText     string
	DeleteAfter time.Time
	// AckFormat selects the acknowledgement: "fb" (fb.media.processed.v1)
	// or "content" (content.media.processed.v1).
	AckFormat string
}

// IsVideo reports whether the job is a video.
func (m *MediaJob) IsVideo() bool {
	switch m.Kind {
	case "video", "reel", "comment_video":
		return true
	}
	return false
}

// Erasure deletes everything of an author or a source.
type Erasure struct {
	RequestID string
	// Subject: author | source.
	Subject   string
	AuthorKey []byte
	SourceID  string
}

// Sentiment labels.
const (
	SentimentPositive = "positive"
	SentimentNegative = "negative"
	SentimentNeutral  = "neutral"
	SentimentMixed    = "mixed"
)

// Result is one item's validated analysis.
type Result struct {
	Lang                  string   `json:"lang"`
	Dialect               string   `json:"dialect"`
	Script                string   `json:"script"`
	CodeSwitch            bool     `json:"code_switch"`
	Sentiment             string   `json:"sentiment"`
	Score                 float64  `json:"score"`
	Confidence            float64  `json:"confidence"`
	CalibratedConfidence  float64  `json:"calibrated_confidence"`
	Entities              []Entity `json:"entities"`
	Topics                []string `json:"topics"`
	Keywords              []string `json:"keywords"`
	Intent                string   `json:"intent"`
	Emotion               string   `json:"emotion"`
	Toxicity              string   `json:"toxicity"`
	Hate                  bool     `json:"hate"`
	Threat                bool     `json:"threat"`
	Harassment            bool     `json:"harassment"`
	Spam                  bool     `json:"spam"`
	Sarcasm               bool     `json:"sarcasm"`
	Urgent                bool     `json:"urgent"`
	PrivatePersonMentions int      `json:"private_person_mentions"`
	Refused               bool     `json:"refused,omitempty"`
}

// Entity is an entity mention with the sentiment toward it.
type Entity struct {
	// ID is the registry id once linked ("" when unknown).
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Sentiment  string  `json:"sentiment"`
	Score      float64 `json:"score"`
	Confidence float64 `json:"confidence"`
	Aspect     string  `json:"aspect"`
	// Origin: text | image | video.
	Origin string `json:"origin,omitempty"`
}

// MediaResult is a validated image or video analysis.
type MediaResult struct {
	Logos       []Logo   `json:"logos"`
	TextInImage string   `json:"text_in_image"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
	PeopleCount int      `json:"people_count"`
	Sentiment   string   `json:"sentiment"`
	Score       float64  `json:"score"`
	Confidence  float64  `json:"confidence"`
	Entities    []Entity `json:"entities"`
	Topics      []string `json:"topics"`
	Transcript  string   `json:"transcript,omitempty"`
}

// Logo is a brand logo seen in an image.
type Logo struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Confidence float64 `json:"confidence"`
}

// Provenance records how a result was produced.
type Provenance struct {
	Provider        string
	Model           string
	PromptVersion   string
	TaxonomyVersion string
	SchemaVersion   string
	Route           string
	Lane            string
	Fallback        bool
	AnalyzedAt      time.Time
	RequestID       string
}

// RouteFor picks the route of an item from its kind and length: short posts
// read like comments.
func RouteFor(it *Item, postShortChars int) string {
	if it.Kind == KindPost && len([]rune(it.Text)) > postShortChars {
		return RoutePost
	}
	return RouteComment
}
