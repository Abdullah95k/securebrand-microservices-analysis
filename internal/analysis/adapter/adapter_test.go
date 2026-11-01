package adapter

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	contentv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/content/v1"
	fbv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/fb/v1"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/topics"
)

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func TestFromFBComment(t *testing.T) {
	text := "علي حسن شبكتكم واقفة"
	c := &fbv1.Comment{CommentId: 7, PostId: 3, PageId: 9, Depth: 1, ParentCommentId: ptr(int64(5)), Text: text,
		CreatedAt: timestamppb.New(t0), PostCreatedAt: timestamppb.New(t0.Add(-time.Hour)), IsOp: ptr(true),
		Author:      &fbv1.Author{AuthorKey: []byte("k"), Name: "must not be copied"},
		TextRanges:  []*fbv1.TextRange{{Offset: 0, Length: 7, EntityType: "User"}, {Offset: 8, Length: 6, EntityType: "Hashtag"}},
		Attachments: []*fbv1.Attachment{{Type: "photo", MediaId: "m1"}, {Type: "sticker"}},
		Obs:         &fbv1.Observation{ObservedAt: timestamppb.New(t0.Add(time.Minute))}}
	it := FromFBComment(c)
	if it.ContentID != "fb:comment:7" || it.Kind != core.KindReply || it.ParentID != "fb:comment:5" || it.ThreadID != "fb:post:3" ||
		it.SourceID != "fb:page:9" || !it.AuthorIsSource || string(it.AuthorKey) != "k" || !it.CreatedAt.Equal(t0) {
		t.Fatalf("mapped %+v", it)
	}
	if len(it.MediaIDs) != 1 || it.MediaIDs[0] != "m1" {
		t.Fatalf("media %v", it.MediaIDs)
	}
	if len(it.RemoveSpans) != 1 || text[it.RemoveSpans[0].Start:it.RemoveSpans[0].End] != "علي حسن" {
		t.Fatalf("tag span %+v", it.RemoveSpans)
	}
	if !it.Analyzable() {
		t.Fatal("text after the tag must be analyzable")
	}
	// Only a tag: nothing to analyse.
	c.Text, c.TextRanges = "علي حسن", []*fbv1.TextRange{{Offset: 0, Length: 7, EntityType: "User"}}
	if FromFBComment(c).Analyzable() {
		t.Fatal("tag-only comment analyzable")
	}
	// Missing creation time: the post's.
	c.CreatedAt = nil
	if got := FromFBComment(c).CreatedAt; !got.Equal(t0.Add(-time.Hour)) {
		t.Fatalf("fallback time %v", got)
	}
	// Deletion and redaction.
	c.Status = fbv1.CommentStatus_COMMENT_STATUS_DELETED
	if FromFBComment(c).Status != core.StatusDeleted {
		t.Fatal("deleted")
	}
	c.Redacted = true
	if it := FromFBComment(c); it.Status != core.StatusRedacted || it.Text != "" || it.MediaIDs != nil {
		t.Fatalf("redacted %+v", it)
	}
}

func TestFromFBPostAddsLinkTitle(t *testing.T) {
	p := &fbv1.Post{PostId: 3, PageId: 9, AuthorName: "Asiacell", CreatedAt: timestamppb.New(t0), Text: "",
		Links: []*fbv1.LinkAttachment{{Title: "عرض جديد"}}, MediaIds: []string{"a"}}
	it := FromFBPost(p)
	if it.ContentID != "fb:post:3" || it.Kind != core.KindPost || it.Text != "عرض جديد" || it.SourceName != "Asiacell" || !it.AuthorIsSource {
		t.Fatalf("post %+v", it)
	}
	p.Text = "خبر"
	if FromFBPost(p).Text != "خبر\nعرض جديد" {
		t.Fatal("link title not appended")
	}
	p.Status = fbv1.PostStatus_POST_STATUS_UNAVAILABLE
	if FromFBPost(p).Status != core.StatusDeleted {
		t.Fatal("unavailable post")
	}
}

func TestFromFBMediaReadyAndErasure(t *testing.T) {
	m := FromFBMediaReady(&fbv1.MediaReady{MediaId: "u1", Kind: fbv1.MediaKind_MEDIA_KIND_COMMENT_PHOTO, PostId: 3, PageId: 9,
		CommentId: ptr(int64(7)), Object: &fbv1.S3Object{Bucket: "b", Key: "k", Size: 10}, DeleteAfter: timestamppb.New(t0)})
	if m.OwnerContentID != "fb:comment:7" || m.Kind != "comment_photo" || m.Bucket != "b" || m.AckFormat != "fb" || m.IsVideo() {
		t.Fatalf("media %+v", m)
	}
	if !(&core.MediaJob{Kind: "reel"}).IsVideo() {
		t.Fatal("reel is a video")
	}
	e := FromFBErasure(&fbv1.ErasureEvent{EventId: "e", SubjectType: fbv1.SubjectType_SUBJECT_TYPE_PAGE, PageId: 9})
	if e == nil || e.Subject != "source" || e.SourceID != "fb:page:9" || e.RequestID != "e" {
		t.Fatalf("page erasure %+v", e)
	}
	if FromFBErasure(&fbv1.ErasureEvent{SubjectType: fbv1.SubjectType_SUBJECT_TYPE_AUTHOR}) != nil {
		t.Fatal("author erasure without key accepted")
	}
	a := FromFBErasure(&fbv1.ErasureEvent{RequestId: "r", SubjectType: fbv1.SubjectType_SUBJECT_TYPE_AUTHOR, AuthorKey: []byte{1, 2}})
	if a == nil || SubjectKey(a) != "0102" || a.RequestID != "r" {
		t.Fatalf("author erasure %+v", a)
	}
}

func TestDecodeCanonicalAndFB(t *testing.T) {
	d := NewDecoder([]string{"fb", "content"})
	if len(d.ItemTopics()) != 5 || len(d.MediaTopics()) != 2 {
		t.Fatalf("topics %v %v", d.ItemTopics(), d.MediaTopics())
	}
	ci := &contentv1.ContentItem{ContentId: "x:comment:1", Platform: contentv1.Platform_PLATFORM_X, Kind: contentv1.ContentKind_CONTENT_KIND_REPLY,
		SourceId: "x:account:2", ObservedAt: timestamppb.New(t0), Text: "@user hi",
		Mentions: []*contentv1.Mention{{Offset: 0, Length: 5, Type: "user"}}, Media: []*contentv1.MediaRef{{MediaId: "m"}}}
	b, _ := proto.Marshal(ci)
	evs, err := d.Decode(topics.ContentItem, b)
	if err != nil || len(evs) != 1 {
		t.Fatalf("decode %v %v", evs, err)
	}
	it := evs[0].Item
	if it.Platform != "x" || it.Kind != core.KindReply || !it.CreatedAt.Equal(t0) || len(it.RemoveSpans) != 1 || len(it.MediaIDs) != 1 {
		t.Fatalf("canonical %+v", it)
	}
	if _, err := d.Decode(topics.ContentItem, mustMarshal(&contentv1.ContentItem{})); err == nil {
		t.Fatal("item without id accepted")
	}
	if _, err := d.Decode(topics.Comment, []byte{0xff, 0xff}); err == nil {
		t.Fatal("garbage decoded")
	}
	if evs, err := d.Decode("other.topic", b); err != nil || evs != nil {
		t.Fatal("unknown topic should decode to nothing")
	}
	only := NewDecoder([]string{"content"})
	if len(only.ItemTopics()) != 2 {
		t.Fatal("content-only topics")
	}
	if PlatformEnum(PlatformName(contentv1.Platform_PLATFORM_TIKTOK)) != contentv1.Platform_PLATFORM_TIKTOK || PlatformName(99) != "unknown" {
		t.Fatal("platform names")
	}
}

func TestRuneSpan(t *testing.T) {
	s := "ab😀cd"
	for _, c := range []struct {
		off, n     int
		ok         bool
		start, end int
	}{
		{0, 2, true, 0, 2}, {2, 1, true, 2, 6}, {3, 2, true, 6, 8}, {4, 1, true, 7, 8},
		{4, 2, false, 0, 0}, {5, 1, false, 0, 0}, {-1, 1, false, 0, 0}, {0, 0, false, 0, 0},
	} {
		sp, ok := runeSpan(s, c.off, c.n)
		if ok != c.ok || (ok && (sp.Start != c.start || sp.End != c.end)) {
			t.Errorf("runeSpan(%d,%d) = %+v %v", c.off, c.n, sp, ok)
		}
	}
}

func mustMarshal(m proto.Message) []byte {
	b, _ := proto.Marshal(m)
	return b
}
