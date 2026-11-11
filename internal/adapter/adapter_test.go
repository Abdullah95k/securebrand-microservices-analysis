package adapter

import (
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/topics"
	fbv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/fb/v1"
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

// Tagged users are found by Facebook's UTF-16 offsets (an emoji before a
// tag counts twice), on posts as well as comments; a range that misses the
// name falls back to the name's own position.
func TestFBTagSpans(t *testing.T) {
	text := "😀 Ali Sara and Example Brand"
	ranges := []*fbv1.TextRange{
		{Offset: 3, Length: 3, EntityType: "User", Name: "Ali"},
		{Offset: 99, Length: 4, EntityType: "User", Name: "Sara"},
		{Offset: 16, Length: 13, EntityType: "Page", Name: "Example Brand"},
	}
	c := FromFBComment(&fbv1.Comment{CommentId: 1, PostId: 2, PageId: 3, Text: text, TextRanges: ranges})
	var got []string
	for _, sp := range c.RemoveSpans {
		got = append(got, text[sp.Start:sp.End])
	}
	if strings.Join(got, ",") != "Ali,Sara" {
		t.Fatalf("comment spans %q", got)
	}
	p := FromFBPost(&fbv1.Post{PostId: 2, PageId: 3, Text: text, TextRanges: ranges[:1]})
	if len(p.RemoveSpans) != 1 || text[p.RemoveSpans[0].Start:p.RemoveSpans[0].End] != "Ali" {
		t.Fatalf("post spans %+v", p.RemoveSpans)
	}
}

func mustMarshal(m proto.Message) []byte {
	b, _ := proto.Marshal(m)
	return b
}

// TestDecoderFBMedia: with "fb-media" only fbs's media announcements and
// erasures are read from its own topics; its posts and comments come
// through the contract (REQ-018), so each is read once.
func TestDecoderFBMedia(t *testing.T) {
	d := NewDecoder([]string{"content", "fb-media"})
	has := func(ts []string, want string) bool {
		for _, x := range ts {
			if x == want {
				return true
			}
		}
		return false
	}
	items, media := d.ItemTopics(), d.MediaTopics()
	for _, want := range []string{topics.Erasure, topics.ContentPost, topics.ContentComment} {
		if !has(items, want) {
			t.Errorf("item topics %v lack %s", items, want)
		}
	}
	for _, not := range []string{topics.Post, topics.Comment} {
		if has(items, not) || has(d.CommentTopics(), not) || has(d.PostTopics(), not) {
			t.Errorf("%s read with fb-media", not)
		}
	}
	if !has(media, topics.MediaReady) || !has(media, topics.ContentMediaReady) {
		t.Errorf("media topics %v", media)
	}
	if full := NewDecoder([]string{"fb"}); !has(full.ItemTopics(), topics.Post) || !has(full.MediaTopics(), topics.MediaReady) {
		t.Error("fb no longer reads everything")
	}
}
