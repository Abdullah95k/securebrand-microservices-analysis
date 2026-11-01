// Package adapter turns input records into canonical analysis events: the
// canonical content.* topics (securebrand.content.v1) natively, and the
// Facebook collector's fb.* topics through the Facebook adapter, so the
// service runs before any normalisation service exists.
package adapter

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	contentv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/content/v1"
	fbv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/fb/v1"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/textnorm"
	"github.com/abdullah95k/securebrand-microservices/internal/topics"
)

// Event is what one record means to the analysis service. Exactly one field
// is set; a record the service ignores decodes to no event.
type Event struct {
	Item    *core.Item
	Media   *core.MediaJob
	Erasure *core.Erasure
}

// Decoder maps topics (without prefix) to their adapter.
type Decoder struct {
	FB      bool
	Content bool
}

// NewDecoder enables the adapters named in inputs ("fb", "content").
func NewDecoder(inputs []string) Decoder {
	var d Decoder
	for _, in := range inputs {
		switch in {
		case "fb":
			d.FB = true
		case "content":
			d.Content = true
		}
	}
	return d
}

// ItemTopics are the topics carrying content items and erasures (ingest).
func (d Decoder) ItemTopics() []string {
	var ts []string
	if d.FB {
		ts = append(ts, topics.Post, topics.Comment, topics.Erasure)
	}
	if d.Content {
		ts = append(ts, topics.ContentItem, topics.ContentErasure)
	}
	return ts
}

// MediaTopics are the topics announcing media files to analyse.
func (d Decoder) MediaTopics() []string {
	var ts []string
	if d.FB {
		ts = append(ts, topics.MediaReady)
	}
	if d.Content {
		ts = append(ts, topics.ContentMediaReady)
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
	case topics.ContentItem:
		var m contentv1.ContentItem
		if err := proto.Unmarshal(value, &m); err != nil {
			return nil, err
		}
		if m.ContentId == "" {
			return nil, fmt.Errorf("content item without content_id")
		}
		return []Event{{Item: FromContentItem(&m)}}, nil
	case topics.ContentMediaReady:
		var m contentv1.MediaReady
		if err := proto.Unmarshal(value, &m); err != nil {
			return nil, err
		}
		return []Event{{Media: FromContentMediaReady(&m)}}, nil
	case topics.ContentErasure:
		var m contentv1.Erasure
		if err := proto.Unmarshal(value, &m); err != nil {
			return nil, err
		}
		if e := FromContentErasure(&m); e != nil {
			return []Event{{Erasure: e}}, nil
		}
		return nil, nil
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
	for _, r := range c.TextRanges {
		if r.EntityType == "User" {
			if sp, ok := runeSpan(c.Text, int(r.Offset), int(r.Length)); ok {
				it.RemoveSpans = append(it.RemoveSpans, sp)
			}
		}
	}
	switch {
	case c.Redacted:
		it.Status, it.Text, it.MediaIDs = core.StatusRedacted, "", nil
	case c.Status == fbv1.CommentStatus_COMMENT_STATUS_DELETED:
		it.Status = core.StatusDeleted
	}
	return it
}

// runeSpan converts a range in runes (as fbs's parser counts them) to bytes.
func runeSpan(s string, off, n int) (textnorm.Span, bool) {
	if off < 0 || n <= 0 {
		return textnorm.Span{}, false
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
	if start < 0 {
		return textnorm.Span{}, false
	}
	if end < 0 {
		// The loop ran to the end, so i is the rune count. A range past the
		// end means the offsets do not fit this text: ignore it rather than
		// risk removing real content.
		if i < off+n {
			return textnorm.Span{}, false
		}
		end = len(s)
	}
	return textnorm.Span{Start: start, End: end}, true
}

// FromFBMediaReady maps a media announcement; acks go to
// fb.media.processed.v1.
func FromFBMediaReady(m *fbv1.MediaReady) *core.MediaJob {
	owner := FBPostID(m.PostId)
	if m.CommentId != nil {
		owner = FBCommentID(*m.CommentId)
	}
	j := &core.MediaJob{
		MediaID:        m.MediaId,
		OwnerContentID: owner,
		Platform:       "facebook",
		SourceID:       FBPageID(m.PageId),
		Kind:           strings.ToLower(strings.TrimPrefix(m.Kind.String(), "MEDIA_KIND_")),
		DurationMS:     m.DurationMs,
		AltText:        m.AltText,
		DeleteAfter:    ts(m.DeleteAfter),
		AckFormat:      "fb",
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

// Canonical ---------------------------------------------------------------------

var platforms = map[contentv1.Platform]string{
	contentv1.Platform_PLATFORM_FACEBOOK:  "facebook",
	contentv1.Platform_PLATFORM_INSTAGRAM: "instagram",
	contentv1.Platform_PLATFORM_TIKTOK:    "tiktok",
	contentv1.Platform_PLATFORM_X:         "x",
	contentv1.Platform_PLATFORM_TELEGRAM:  "telegram",
	contentv1.Platform_PLATFORM_YOUTUBE:   "youtube",
}

// PlatformName returns the store's name of a platform.
func PlatformName(p contentv1.Platform) string {
	if n, ok := platforms[p]; ok {
		return n
	}
	return "unknown"
}

// PlatformEnum is the inverse of PlatformName.
func PlatformEnum(name string) contentv1.Platform {
	for k, v := range platforms {
		if v == name {
			return k
		}
	}
	return contentv1.Platform_PLATFORM_UNSPECIFIED
}

// KindEnum maps a store kind to the contract's enum.
func KindEnum(kind string) contentv1.ContentKind {
	switch kind {
	case core.KindPost:
		return contentv1.ContentKind_CONTENT_KIND_POST
	case core.KindComment:
		return contentv1.ContentKind_CONTENT_KIND_COMMENT
	case core.KindReply:
		return contentv1.ContentKind_CONTENT_KIND_REPLY
	}
	return contentv1.ContentKind_CONTENT_KIND_UNSPECIFIED
}

// FromContentItem maps the canonical contract.
func FromContentItem(m *contentv1.ContentItem) *core.Item {
	it := &core.Item{
		ContentID:      m.ContentId,
		Platform:       PlatformName(m.Platform),
		Kind:           core.KindComment,
		SourceID:       m.SourceId,
		SourceName:     m.SourceName,
		ThreadID:       m.ThreadId,
		ParentID:       m.ParentId,
		AuthorKey:      m.AuthorKey,
		AuthorIsSource: m.AuthorIsSource,
		CreatedAt:      ts(m.CreatedAt),
		ObservedAt:     ts(m.ObservedAt),
		Text:           m.Text,
		LangHint:       m.LangHint,
		Hashtags:       m.Hashtags,
		Status:         core.StatusVisible,
	}
	switch m.Kind {
	case contentv1.ContentKind_CONTENT_KIND_POST:
		it.Kind = core.KindPost
	case contentv1.ContentKind_CONTENT_KIND_REPLY:
		it.Kind = core.KindReply
	}
	if it.CreatedAt.IsZero() {
		it.CreatedAt = it.ObservedAt
	}
	for _, md := range m.Media {
		if md.MediaId != "" {
			it.MediaIDs = append(it.MediaIDs, md.MediaId)
		}
	}
	for _, mn := range m.Mentions {
		if mn.Type == "user" {
			if sp, ok := runeSpan(m.Text, int(mn.Offset), int(mn.Length)); ok {
				it.RemoveSpans = append(it.RemoveSpans, sp)
			}
		}
	}
	switch m.Status {
	case contentv1.ContentStatus_CONTENT_STATUS_DELETED:
		it.Status = core.StatusDeleted
	case contentv1.ContentStatus_CONTENT_STATUS_REDACTED:
		it.Status, it.Text, it.MediaIDs = core.StatusRedacted, "", nil
	}
	return it
}

// FromContentMediaReady maps a canonical media announcement; acks go to
// content.media.processed.v1.
func FromContentMediaReady(m *contentv1.MediaReady) *core.MediaJob {
	return &core.MediaJob{
		MediaID:        m.MediaId,
		OwnerContentID: m.OwnerContentId,
		Platform:       PlatformName(m.Platform),
		SourceID:       m.SourceId,
		Kind:           strings.ToLower(strings.TrimPrefix(m.Kind.String(), "MEDIA_KIND_")),
		Bucket:         m.Bucket,
		Key:            m.Key,
		Size:           m.Size,
		ContentType:    m.ContentType,
		DurationMS:     m.DurationMs,
		AltText:        m.AltText,
		DeleteAfter:    ts(m.DeleteAfter),
		AckFormat:      "content",
	}
}

// FromContentErasure maps a canonical erasure.
func FromContentErasure(e *contentv1.Erasure) *core.Erasure {
	switch e.Subject {
	case contentv1.ErasureSubject_ERASURE_SUBJECT_AUTHOR:
		if len(e.AuthorKey) == 0 {
			return nil
		}
		return &core.Erasure{RequestID: requestID(e.RequestId, e.EventId), Subject: "author", AuthorKey: e.AuthorKey}
	case contentv1.ErasureSubject_ERASURE_SUBJECT_SOURCE:
		if e.SourceId == "" {
			return nil
		}
		return &core.Erasure{RequestID: requestID(e.RequestId, e.EventId), Subject: "source", SourceID: e.SourceId}
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
