// Package adapter turns input records into analysis events: the platform's
// contract (content.post.v1 and content.comment.v1, contract.go) and the
// Facebook collector's own fb.* topics (the Facebook adapter).
package adapter

import (
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/textnorm"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/topics"
	fbv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/fb/v1"
)

// Event is what one record means to the analysis service. Exactly one field
// is set; a record the service ignores decodes to no event.
type Event struct {
	Item      *core.Item
	Media     *core.MediaJob
	MediaGone *core.MediaGone
	Erasure   *core.Erasure
}

// Decoder maps topics (without prefix) to their adapter.
type Decoder struct {
	FB      bool
	Content bool
	// FBMedia (input "fb-media"): of fbs's own topics, only its media
	// announcements (fb.media.ready.v1, acknowledged on
	// fb.media.processed.v1) and erasures (fb.erasure.v1). fbs's posts and
	// comments come through the contract (REQ-018): read on both paths,
	// one with a different hash (link titles, media names) would be
	// analysed again on every re-scrape. FB includes all of these.
	FBMedia bool
}

// NewDecoder enables the adapters named in inputs: "fb" (fbs's own topics)
// and "content" (the platform's contract).
func NewDecoder(inputs []string) Decoder {
	var d Decoder
	for _, in := range inputs {
		switch in {
		case "fb":
			d.FB = true
		case "fb-media":
			d.FBMedia = true
		case "content":
			d.Content = true
		}
	}
	return d
}

// ItemTopics are the topics carrying content items and erasures (ingest).
func (d Decoder) ItemTopics() []string {
	var ts []string
	switch {
	case d.FB:
		ts = append(ts, topics.Post, topics.Comment, topics.Erasure)
	case d.FBMedia:
		ts = append(ts, topics.Erasure)
	}
	if d.Content {
		ts = append(ts, topics.ContentPost, topics.ContentComment)
	}
	return ts
}

// ContentTopics are the item topics without the erasure topics: the ones
// ingest pauses while the analysis queue is full (erasures keep flowing).
func (d Decoder) ContentTopics() []string {
	var ts []string
	if d.FB {
		ts = append(ts, topics.Post, topics.Comment)
	}
	if d.Content {
		ts = append(ts, topics.ContentPost, topics.ContentComment)
	}
	return ts
}

// CommentTopics are the topics of comments and replies: the many items.
// While the analysis queue is full only these wait in Kafka; posts are few,
// and their media and captions wait for them.
func (d Decoder) CommentTopics() []string {
	var ts []string
	if d.FB {
		ts = append(ts, topics.Comment)
	}
	if d.Content {
		ts = append(ts, topics.ContentComment)
	}
	return ts
}

// PostTopics are the topics of posts: they wait in Kafka only at the
// queue's much higher post cap (analysis.backpressure.max_queue_posts).
func (d Decoder) PostTopics() []string {
	var ts []string
	if d.FB {
		ts = append(ts, topics.Post)
	}
	if d.Content {
		ts = append(ts, topics.ContentPost)
	}
	return ts
}

// MediaTopics are the topics announcing media files to analyse, and the
// contract's announcements of deleted files.
func (d Decoder) MediaTopics() []string {
	var ts []string
	if d.FB || d.FBMedia {
		ts = append(ts, topics.MediaReady)
	}
	if d.Content {
		ts = append(ts, topics.ContentMediaReady, topics.ContentMediaDeleted)
	}
	return ts
}

// Decode decodes one record of base topic (prefix removed).
func (d Decoder) Decode(topic string, value []byte) ([]Event, error) {
	switch topic {
	case topics.Post:
		var m fbv1.Post
		if err := proto.Unmarshal(value, &m); err != nil {
			return nil, err
		}
		return []Event{{Item: FromFBPost(&m)}}, nil
	case topics.Comment:
		var m fbv1.Comment
		if err := proto.Unmarshal(value, &m); err != nil {
			return nil, err
		}
		return []Event{{Item: FromFBComment(&m)}}, nil
	case topics.MediaReady:
		var m fbv1.MediaReady
		if err := proto.Unmarshal(value, &m); err != nil {
			return nil, err
		}
		return []Event{{Media: FromFBMediaReady(&m)}}, nil
	case topics.Erasure:
		var m fbv1.ErasureEvent
		if err := proto.Unmarshal(value, &m); err != nil {
			return nil, err
		}
		if e := FromFBErasure(&m); e != nil {
			return []Event{{Erasure: e}}, nil
		}
		return nil, nil
	case topics.ContentPost, topics.ContentComment:
		return decodeContract(topic, value)
	case topics.ContentMediaReady, topics.ContentMediaDeleted:
		return decodeContractMedia(topic, value)
	}
	return nil, nil
}

// Facebook ---------------------------------------------------------------------

// FB content ids.
func FBPostID(id int64) string    { return "fb:post:" + strconv.FormatInt(id, 10) }
func FBCommentID(id int64) string { return "fb:comment:" + strconv.FormatInt(id, 10) }
func FBPageID(id int64) string    { return "fb:page:" + strconv.FormatInt(id, 10) }

// FromFBPost maps a Facebook post: the page is both source and author.
func FromFBPost(p *fbv1.Post) *core.Item {
	it := &core.Item{
		ContentID:      FBPostID(p.PostId),
		Platform:       "facebook",
		Kind:           core.KindPost,
		SourceID:       FBPageID(p.PageId),
		SourceName:     p.AuthorName,
		AuthorIsSource: true,
		CreatedAt:      ts(p.CreatedAt),
		ObservedAt:     ts(p.GetObs().GetObservedAt()),
		Text:           postText(p),
		Hashtags:       p.Hashtags,
		MediaIDs:       p.MediaIds,
		Status:         core.StatusVisible,
		// Spans index p.Text, the start of the text: the link title is
		// appended after it.
		RemoveSpans: FBTagSpans(p.Text, p.TextRanges),
	}
	if p.Status == fbv1.PostStatus_POST_STATUS_UNAVAILABLE {
		it.Status = core.StatusDeleted
	}
	return it
}

// postText is the post's text plus the title of a link it shares: a link
// post's message is often empty and the title carries the topic.
func postText(p *fbv1.Post) string {
	text := p.Text
	for _, l := range p.Links {
		if l.Title != "" && !strings.Contains(text, l.Title) {
			if text != "" {
				text += "\n"
			}
			text += l.Title
			break
		}
	}
	return text
}

// FromFBComment maps a comment or a reply. Tagged users carry no content of
// their own: their spans are excluded from the analyzable check.
func FromFBComment(c *fbv1.Comment) *core.Item {
	it := &core.Item{
		ContentID:      FBCommentID(c.CommentId),
		Platform:       "facebook",
		Kind:           core.KindComment,
		SourceID:       FBPageID(c.PageId),
		ThreadID:       FBPostID(c.PostId),
		ParentID:       FBPostID(c.PostId),
		AuthorKey:      c.GetAuthor().GetAuthorKey(),
		AuthorIsSource: c.GetIsOp(),
		CreatedAt:      ts(c.CreatedAt),
		ObservedAt:     ts(c.GetObs().GetObservedAt()),
		Text:           c.Text,
		Status:         core.StatusVisible,
	}
	if c.Depth > 0 {
		it.Kind = core.KindReply
		if c.ParentCommentId != nil {
			it.ParentID = FBCommentID(*c.ParentCommentId)
		}
	}
	if it.CreatedAt.IsZero() {
		// Facebook sometimes omits a comment's time: the post's time keeps
		// it in the right month, and the observation is the upper bound.
		it.CreatedAt = ts(c.PostCreatedAt)
		if it.CreatedAt.IsZero() {
			it.CreatedAt = it.ObservedAt
		}
	}
	for _, a := range c.Attachments {
		if a.MediaId != "" {
			it.MediaIDs = append(it.MediaIDs, a.MediaId)
		}
	}
	it.RemoveSpans = FBTagSpans(c.Text, c.TextRanges)
	switch {
	case c.Redacted:
		it.Status, it.Text, it.MediaIDs = core.StatusRedacted, "", nil
	case c.Status == fbv1.CommentStatus_COMMENT_STATUS_DELETED:
		it.Status = core.StatusDeleted
	}
	return it
}

// FBTagSpans returns the byte spans of the accounts tagged in a Facebook
// text (ranges of type User). Facebook counts offsets in UTF-16 units. When
// a range does not land on the tagged name (the text and its ranges
// disagree), the name's first unused occurrence is taken instead, so a
// name is never left visible because of an offset slip.
func FBTagSpans(text string, ranges []*fbv1.TextRange) []textnorm.Span {
	var out []textnorm.Span
	used := map[int]bool{}
	for _, r := range ranges {
		if r.EntityType != "User" {
			continue
		}
		sp, ok := textnorm.UTF16Span(text, int(r.Offset), int(r.Length))
		if r.Name != "" && (!ok || text[sp.Start:sp.End] != r.Name) {
			ok = false
			for from := 0; from < len(text); {
				i := strings.Index(text[from:], r.Name)
				if i < 0 {
					break
				}
				if !used[from+i] {
					sp, ok = textnorm.Span{Start: from + i, End: from + i + len(r.Name)}, true
					break
				}
				from += i + len(r.Name)
			}
		}
		if ok && !used[sp.Start] {
			used[sp.Start] = true
			out = append(out, sp)
		}
	}
	return out
}

// FromFBMediaReady maps a media announcement; acks go to
// fb.media.processed.v1.
//
// A page's own picture (fbs's page profile picture and cover, since 13 Nov:
// owner_type page, no post or comment) belongs to its page: it doesn't wait
// for a post, and it takes the role of a profile picture or banner, so its
// request says what it is and that no one in it is to be identified
// (D-061). fbs sends monitored pages' pictures only.
func FromFBMediaReady(m *fbv1.MediaReady) *core.MediaJob {
	owner := FBPostID(m.PostId)
	role := ""
	switch {
	case m.CommentId != nil:
		owner = FBCommentID(*m.CommentId)
	case m.PostId == 0:
		owner = FBPageID(m.PageId)
		if m.Kind == fbv1.MediaKind_MEDIA_KIND_COVER {
			role = "banner"
		}
	}
	if m.Kind == fbv1.MediaKind_MEDIA_KIND_PROFILE_PIC {
		role = "profile_picture"
	}
	j := &core.MediaJob{
		MediaID:        m.MediaId,
		OwnerContentID: owner,
		Platform:       "facebook",
		SourceID:       FBPageID(m.PageId),
		Kind:           strings.ToLower(strings.TrimPrefix(m.Kind.String(), "MEDIA_KIND_")),
		Role:           role,
		DurationMS:     m.DurationMs,
		AltText:        m.AltText,
		DeleteAfter:    ts(m.DeleteAfter),
		AckFormat:      core.AckFB,
	}
	if o := m.Object; o != nil {
		j.Bucket, j.Key, j.Size, j.ContentType = o.Bucket, o.Key, o.Size, o.ContentType
	}
	return j
}

// FromFBErasure maps an erasure of an author (by key) or a page.
func FromFBErasure(e *fbv1.ErasureEvent) *core.Erasure {
	switch e.SubjectType {
	case fbv1.SubjectType_SUBJECT_TYPE_AUTHOR:
		if len(e.AuthorKey) == 0 {
			return nil
		}
		return &core.Erasure{RequestID: requestID(e.RequestId, e.EventId), Subject: "author", AuthorKey: e.AuthorKey}
	case fbv1.SubjectType_SUBJECT_TYPE_PAGE:
		if e.PageId == 0 {
			return nil
		}
		return &core.Erasure{RequestID: requestID(e.RequestId, e.EventId), Subject: "source", SourceID: FBPageID(e.PageId)}
	}
	return nil
}

func requestID(req, event string) string {
	if req != "" {
		return req
	}
	return event
}

func ts(t *timestamppb.Timestamp) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.AsTime().UTC()
}

// SubjectKey is how an erased subject is recorded: hex author key or source
// id.
func SubjectKey(e *core.Erasure) string {
	if e.Subject == "author" {
		return hex.EncodeToString(e.AuthorKey)
	}
	return e.SourceID
}
