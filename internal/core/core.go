// Package core holds the analysis service's domain types, shared by the
// adapters, the store, the pipeline and the evaluation harness.
package core

import (
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/textnorm"
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

// Item is a content item, decoded by an adapter from the platform's contract
// (content.post.v1, content.comment.v1) or from fbs's own fb.* topics.
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
	// From the platform's contract (zero for fbs's own topics): the
	// envelope.version that brought this text, a comment's depth (0 on the
	// post, 1+ for replies), and the author's account ID ("" when the
	// platform hides the author).
	Version  int64
	Depth    int
	AuthorID string
	// Captions: a post's caption track, used as its video's transcript
	// (the uploader's captions first, then the platform's speech-to-text;
	// never a machine translation), with its language and source.
	Captions       string
	CaptionsLang   string
	CaptionsSource string
	// Backfill: the collector sent it from a backfill (the contract's
	// provenance.mode CRAWL_MODE_BACKFILL), not as live work, so it is
	// analysed behind live items (Pipeline.Plan). Not stored.
	Backfill bool
}

// PlatformCode is the contract's platform code of a contract ID
// ("fb:comment:1" -> "fb"); "" when the ID has no code.
func PlatformCode(id string) string {
	for i := 0; i < len(id); i++ {
		if id[i] == ':' {
			return id[:i]
		}
	}
	return ""
}

// FromContract reports whether the item came through the platform's
// contract (it has an envelope version), so its results may be published
// on the contract's topics.
func (it *Item) FromContract() bool { return it.Version > 0 }

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
	// AckFormat selects the acknowledgement: AckFB (fb.media.processed.v1)
	// or AckContract (the contract's content.media.processed.v1).
	AckFormat string
	// From the contract (content.media.ready.v1): the media's role
	// (content, attachment, cover, thumbnail...; Kind holds its type), the
	// file's hex SHA-256 and the announcement's attempt (1 for the first).
	Role    string
	Sha256  string
	Attempt int
	// ReceivedAt is when the file was first announced, and Tries how many
	// times it has been claimed, this time included (both set when claimed).
	ReceivedAt time.Time
	Tries      int
}

// Media acknowledgement formats.
const (
	AckFB       = "fb"
	AckContract = "contract"
)

// MediaGone says a media file was deleted from storage
// (content.media.deleted.v1): a job still waiting for it can't run.
type MediaGone struct {
	MediaID string
	Reason  string
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
	// AdType: own | paid | hidden | sale, "" when the item is not an ad.
	AdType string `json:"ad_type,omitempty"`
	// Legal lists possible legal or platform-policy breaches (for review).
	Legal []string `json:"legal,omitempty"`
	// NewTopics are the model's proposals for topics missing from the
	// taxonomy; the pipeline resolves them into Topics before storage.
	NewTopics []TopicProposal `json:"-"`
}

// TopicProposal is a topic the model found missing from the taxonomy.
type TopicProposal struct {
	EN     string `json:"en"`
	AR     string `json:"ar"`
	Parent string `json:"parent"`
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
	// TranscriptLang and TranscriptSource: the transcript's language, and
	// where it came from ("platform_captions", or the speech-to-text
	// model).
	TranscriptLang   string   `json:"transcript_lang,omitempty"`
	TranscriptSource string   `json:"transcript_source,omitempty"`
	AdType           string   `json:"ad_type,omitempty"`
	Legal            []string `json:"legal,omitempty"`
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
	// Routing: why the item took its lane (urgent_source, tracked_entity or
	// normal); MatchedEntityIDs: the tracked entities found in it.
	Routing          string
	MatchedEntityIDs []string
}

// RouteFor picks the route of an item from its kind and length: short posts
// read like comments.
func RouteFor(it *Item, postShortChars int) string {
	if it.Kind == KindPost && len([]rune(it.Text)) > postShortChars {
		return RoutePost
	}
	return RouteComment
}

// AuthorSignals is an author's behaviour over the bot-scoring window.
// Shares are of the author's items in the window.
type AuthorSignals struct {
	Platform   string
	AuthorKey  []byte
	Items      int
	ActiveDays int
	// MaxPerHour is the most items in one clock hour; ActiveHours the
	// distinct hours of the day (Iraq time) the author posted in.
	MaxPerHour  int
	Sources     int
	ActiveHours int
	// Repeated: the same substantial text posted in three or more threads.
	Repeated float64
	// Coordinated: a substantial text that several other authors posted on
	// the same day; CoordinatedItems counts them.
	Coordinated      float64
	CoordinatedItems int
	// Fast: comments within a minute of their post (of FastBase comments
	// whose post is known).
	Fast     float64
	FastBase int
	// Spammy: spam, advertising or scams according to the analyses.
	Spammy              float64
	FirstSeen, LastSeen time.Time
}

// PerDay is the author's average items per active day.
func (s AuthorSignals) PerDay() float64 { return float64(s.Items) / float64(max(s.ActiveDays, 1)) }
