package adapter

import (
	"fmt"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"
	"gopkg.in/yaml.v3"

	contracts "github.com/abdullah95k/securebrand-contracts"
	contentv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/content/v1"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/textnorm"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/topics"
)

// The platform's contract (github.com/abdullah95k/securebrand-contracts):
// content.post.v1 and content.comment.v1 become items.

// decodeContract decodes a contract record. A partial event (only some
// fields set: a deletion, a status change) is never an edit: it yields a
// status change when it says the item is no longer visible, else nothing.
func decodeContract(topic string, value []byte) ([]Event, error) {
	var it *core.Item
	switch topic {
	case topics.ContentPost:
		var m contentv1.Post
		if err := proto.Unmarshal(value, &m); err != nil {
			return nil, err
		}
		if m.GetEnvelope().GetId() == "" {
			return nil, fmt.Errorf("post without envelope.id")
		}
		it = FromContractPost(&m)
	case topics.ContentComment:
		var m contentv1.Comment
		if err := proto.Unmarshal(value, &m); err != nil {
			return nil, err
		}
		if m.GetEnvelope().GetId() == "" {
			return nil, fmt.Errorf("comment without envelope.id")
		}
		it = FromContractComment(&m)
	}
	if it == nil {
		return nil, nil
	}
	return []Event{{Item: it}}, nil
}

// FromContractPost maps a post: its title, text and description joined in
// that order, the account's name (for per-entity sentiment), and the spans
// of mentioned accounts, which are masked before text leaves.
func FromContractPost(m *contentv1.Post) *core.Item {
	env := m.GetEnvelope()
	it := &core.Item{
		ContentID:  env.GetId(),
		Platform:   PlatformName(env.GetPlatform()),
		Kind:       core.KindPost,
		SourceID:   m.GetAccountId(),
		SourceName: accountName(m.GetAccount()),
		ThreadID:   env.GetId(),
		CreatedAt:  ts(m.GetPublishedAt().GetAt()),
		ObservedAt: ts(env.GetObservedAt()),
		Hashtags:   m.GetHashtags(),
		Version:    env.GetVersion(),
		Status:     status(m.GetStatus()),
	}
	if a := m.GetAuthor().GetId(); a != "" {
		it.AuthorID = a
	} else {
		it.AuthorID = m.GetAccountId() // a post is the account's own
	}
	it.AuthorKey = []byte(it.AuthorID)
	it.AuthorIsSource = it.AuthorID == it.SourceID
	if env.GetPartial() {
		return partial(it)
	}
	it.Text, it.RemoveSpans = joinTexts(m.GetTitle(), m.GetText(), m.GetDescription())
	it.LangHint = firstNonEmpty(m.GetText().GetPlatformLanguage(), m.GetTitle().GetPlatformLanguage(), m.GetDescription().GetPlatformLanguage())
	for _, md := range m.GetMedia() {
		if md.GetMediaId() != "" {
			it.MediaIDs = append(it.MediaIDs, md.GetMediaId())
		}
	}
	return finish(it)
}

// FromContractComment maps a comment or a reply (depth 1 or more).
func FromContractComment(m *contentv1.Comment) *core.Item {
	env := m.GetEnvelope()
	it := &core.Item{
		ContentID:  env.GetId(),
		Platform:   PlatformName(env.GetPlatform()),
		Kind:       core.KindComment,
		SourceID:   m.GetAccountId(),
		ThreadID:   m.GetPostId(),
		ParentID:   m.GetPostId(),
		CreatedAt:  ts(m.GetPublishedAt().GetAt()),
		ObservedAt: ts(env.GetObservedAt()),
		Version:    env.GetVersion(),
		Depth:      int(m.GetDepth()),
		Status:     status(m.GetStatus()),
	}
	if m.GetDepth() > 0 {
		it.Kind = core.KindReply
		if p := m.GetParentCommentId(); p != "" {
			it.ParentID = p
		}
	}
	// A hidden author stays unknown: no ID, and no author for bot scoring.
	if a := m.GetAuthor(); a != nil && !a.GetHidden() {
		it.AuthorID = a.GetAccount().GetId()
		it.AuthorKey = []byte(it.AuthorID)
		it.AuthorIsSource = it.AuthorID != "" && it.AuthorID == it.SourceID
	}
	if len(it.AuthorKey) == 0 {
		it.AuthorKey = nil
	}
	if env.GetPartial() {
		return partial(it)
	}
	it.Text, it.RemoveSpans = joinTexts(m.GetText())
	it.LangHint = m.GetText().GetPlatformLanguage()
	for _, md := range m.GetAttachments() {
		if md.GetMediaId() != "" {
			it.MediaIDs = append(it.MediaIDs, md.GetMediaId())
		}
	}
	return finish(it)
}

// partial keeps a partial event only when it says the item is gone; any
// other partial event (counts, a field update) never touches the text.
func partial(it *core.Item) *core.Item {
	if it.Status == core.StatusDeleted {
		// No publish time on a partial event; the stored item's own time
		// is used when it is marked.
		return finish(it)
	}
	return nil
}

func finish(it *core.Item) *core.Item {
	if it.CreatedAt.IsZero() {
		it.CreatedAt = it.ObservedAt
	}
	return it
}

// status maps availability: anything but visible counts as gone (the text
// already stored is kept).
func status(s *contentv1.Status) string {
	switch s.GetAvailability() {
	case contentv1.Availability_AVAILABILITY_UNSPECIFIED, contentv1.Availability_AVAILABILITY_VISIBLE:
		return core.StatusVisible
	}
	return core.StatusDeleted
}

func accountName(a *contentv1.AccountRef) string {
	return firstNonEmpty(a.GetDisplayName(), a.GetHandle())
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// joinTexts joins the non-empty parts with blank lines, in order, and
// returns the mention spans as byte ranges of the joined text, each
// converted from its own unit (UTF-16, code points or bytes). A span that
// falls outside its part or cuts a character is dropped.
func joinTexts(parts ...*contentv1.Text) (string, []textnorm.Span) {
	var b strings.Builder
	var spans []textnorm.Span
	for _, p := range parts {
		t := p.GetText()
		if strings.TrimSpace(t) == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		base := b.Len()
		b.WriteString(t)
		for _, sp := range p.GetSpans() {
			if sp.GetType() != contentv1.SpanType_SPAN_TYPE_MENTION {
				continue
			}
			if s, ok := spanBytes(t, sp); ok {
				spans = append(spans, textnorm.Span{Start: base + s.Start, End: base + s.End})
			}
		}
	}
	return b.String(), spans
}

func spanBytes(text string, sp *contentv1.TextSpan) (textnorm.Span, bool) {
	off, n := int(sp.GetOffset()), int(sp.GetLength())
	switch sp.GetUnit() {
	case contentv1.OffsetUnit_OFFSET_UNIT_CODE_POINT:
		return textnorm.CodePointSpan(text, off, n)
	case contentv1.OffsetUnit_OFFSET_UNIT_BYTE:
		return textnorm.ByteSpan(text, off, n)
	default:
		// UTF-16 is the contract's default (Facebook and TikTok count so).
		return textnorm.UTF16Span(text, off, n)
	}
}

var platformNames = sync.OnceValue(func() map[string]string {
	var reg struct {
		Platforms []struct {
			Code string `yaml:"code"`
			Name string `yaml:"name"`
		} `yaml:"platforms"`
	}
	out := map[string]string{}
	if err := yaml.Unmarshal(contracts.PlatformsYAML, &reg); err != nil {
		return out
	}
	for _, p := range reg.Platforms {
		out[p.Code] = strings.ToLower(p.Name)
	}
	return out
})

// PlatformName is the name sb-analysis stores for a contract platform code
// ("fb" -> "facebook", from the contract's registry/platforms.yaml); an
// unknown code is kept as it is.
func PlatformName(code string) string {
	if n, ok := platformNames()[code]; ok {
		return n
	}
	return code
}
