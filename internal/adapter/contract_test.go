package adapter

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	contentv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/content/v1"
	mediav1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/media/v1"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/topics"
)

// contractDir is the contract module this build uses (its samples come
// with it).
func contractDir(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/abdullah95k/securebrand-contracts").Output()
	if err != nil {
		t.Fatalf("contract module: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func sample(t *testing.T, name string, m proto.Message) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(contractDir(t), "samples", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := protojson.Unmarshal(b, m); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// The contract's own samples pass through the mapping.
func TestContractSamples(t *testing.T) {
	var post contentv1.Post
	sample(t, "post.tt.json", &post)
	p := FromContractPost(&post)
	if p == nil || p.ContentID != post.GetEnvelope().GetId() || p.Platform != "tiktok" || p.Kind != core.KindPost ||
		p.SourceID != post.GetAccountId() || p.Version != post.GetEnvelope().GetVersion() || p.Version <= 0 || p.CreatedAt.IsZero() {
		t.Fatalf("post sample: %+v", p)
	}
	if want := accountName(post.GetAccount()); want == "" || p.SourceName != want {
		t.Fatalf("post account name %q, want %q", p.SourceName, want)
	}
	if p.Status != core.StatusVisible || p.Text == "" {
		t.Fatalf("post sample text/status: %q %q", p.Text, p.Status)
	}

	var reply contentv1.Comment
	sample(t, "comment.yt.json", &reply)
	c := FromContractComment(&reply)
	if c == nil || c.ContentID != reply.GetEnvelope().GetId() || c.Platform != "youtube" || c.ThreadID != reply.GetPostId() ||
		c.Depth != int(reply.GetDepth()) || c.Version <= 0 {
		t.Fatalf("comment sample: %+v", c)
	}
	if reply.GetDepth() > 0 && (c.Kind != core.KindReply || c.ParentID != reply.GetParentCommentId()) {
		t.Fatalf("reply sample: kind %s parent %s", c.Kind, c.ParentID)
	}
	if !reply.GetAuthor().GetHidden() && c.AuthorID != reply.GetAuthor().GetAccount().GetId() {
		t.Fatalf("author %q", c.AuthorID)
	}

	// A partial deletion marks the item gone; it is never an edit.
	var del contentv1.Comment
	sample(t, "comment.fb.deleted.json", &del)
	if !del.GetEnvelope().GetPartial() {
		t.Fatal("the deleted sample is no longer partial")
	}
	d := FromContractComment(&del)
	if d == nil || d.Status != core.StatusDeleted || d.Text != "" || d.Platform != "facebook" {
		t.Fatalf("deleted sample: %+v", d)
	}
}

func text(s string, spans ...*contentv1.TextSpan) *contentv1.Text {
	return &contentv1.Text{Text: s, Spans: spans}
}

func mention(off, n int32, unit contentv1.OffsetUnit) *contentv1.TextSpan {
	return &contentv1.TextSpan{Type: contentv1.SpanType_SPAN_TYPE_MENTION, Offset: off, Length: n, Unit: unit}
}

// Title, text and description join in that order; each part's mention
// spans are converted from their own unit and shifted to the joined text.
func TestJoinTextsAndSpanUnits(t *testing.T) {
	title := text("🔥 Zed", mention(3, 3, contentv1.OffsetUnit_OFFSET_UNIT_UTF16)) // the emoji is 2 UTF-16 units
	body := text("مرحبا Ali", mention(6, 3, contentv1.OffsetUnit_OFFSET_UNIT_CODE_POINT))
	desc := text("by Bo", mention(3, 2, contentv1.OffsetUnit_OFFSET_UNIT_BYTE),
		&contentv1.TextSpan{Type: contentv1.SpanType_SPAN_TYPE_HASHTAG, Offset: 0, Length: 2}, // not a mention
		mention(4, 9, contentv1.OffsetUnit_OFFSET_UNIT_BYTE))                                  // outside the part: dropped
	got, spans := joinTexts(title, text("  "), body, desc)
	if got != "🔥 Zed\n\nمرحبا Ali\n\nby Bo" {
		t.Fatalf("joined %q", got)
	}
	var names []string
	for _, sp := range spans {
		names = append(names, got[sp.Start:sp.End])
	}
	if strings.Join(names, ",") != "Zed,Ali,Bo" {
		t.Fatalf("spans %v -> %v", spans, names)
	}
	// A byte span cutting a character is dropped.
	if _, sp := joinTexts(text("مرحبا", mention(1, 2, contentv1.OffsetUnit_OFFSET_UNIT_BYTE))); len(sp) != 0 {
		t.Fatalf("a span cutting a character was kept: %v", sp)
	}
}

func TestContractPartialAndHiddenAuthor(t *testing.T) {
	env := &contentv1.Envelope{Id: "fb:comment:7", Platform: "fb", Version: 3, Partial: true, ObservedAt: timestamppb.Now()}
	// A partial update of something else (counts) is ignored.
	if it := FromContractComment(&contentv1.Comment{Envelope: env, PostId: "fb:post:1", AccountId: "fb:account:1"}); it != nil {
		t.Fatalf("a partial update became an item: %+v", it)
	}
	env.Partial = false
	it := FromContractComment(&contentv1.Comment{Envelope: env, PostId: "fb:post:1", AccountId: "fb:account:1",
		Author: &contentv1.Author{Hidden: true, Account: &contentv1.AccountRef{Id: "fb:account:9"}}, Text: text("hello")})
	if it == nil || it.AuthorID != "" || it.AuthorKey != nil || it.Text != "hello" || it.Version != 3 {
		t.Fatalf("hidden author: %+v", it)
	}
}

func TestDecodeContractAndFB(t *testing.T) {
	d := NewDecoder([]string{"fb", "content"})
	if len(d.ItemTopics()) != 5 || len(d.ContentTopics()) != 4 || len(d.MediaTopics()) != 3 {
		t.Fatalf("topics %v %v %v", d.ItemTopics(), d.ContentTopics(), d.MediaTopics())
	}
	b, _ := proto.Marshal(&contentv1.Post{Envelope: &contentv1.Envelope{Id: "tt:post:1", Platform: "tt", Version: 1,
		ObservedAt: timestamppb.Now()}, AccountId: "tt:account:2", Text: text("hi")})
	evs, err := d.Decode(topics.ContentPost, b)
	if err != nil || len(evs) != 1 || evs[0].Item.ContentID != "tt:post:1" {
		t.Fatalf("decode %v %v", evs, err)
	}
	if _, err := d.Decode(topics.ContentComment, mustMarshal(&contentv1.Comment{})); err == nil {
		t.Fatal("a comment without envelope.id accepted")
	}
	if PlatformName("yt") != "youtube" || PlatformName("zz") != "zz" {
		t.Fatal("platform names from the contract's registry")
	}
}

// TestContractMediaSample maps the contract's own media_ready sample: a
// TikTok cover image.
func TestContractMediaSample(t *testing.T) {
	var m mediav1.MediaReady
	sample(t, "media_ready.tt.json", &m)
	j := FromContractMediaReady(&m)
	if j.MediaID != "tt:media:00000000-0000-5000-8000-000000000001" || j.OwnerContentID != "tt:post:7400000000000000001" ||
		j.SourceID != "tt:account:7000000000000000001" || j.Platform != "tiktok" || j.Kind != "image" || j.Role != "cover" ||
		j.Bucket != "media" || j.Key != "tt/00/00/cover.jpeg" || j.Size != 430000 || j.Attempt != 1 || j.IsVideo() ||
		j.Sha256 != "aa00000000000000000000000000000000000000000000000000000000000001" || j.AckFormat != core.AckContract ||
		j.DeleteAfter.IsZero() {
		t.Fatalf("job %+v", j)
	}
}

func TestDecodeContractMedia(t *testing.T) {
	d := NewDecoder([]string{"content"})
	b, _ := proto.Marshal(&mediav1.MediaReady{EventId: "e1", MediaId: "fb:media:1", Platform: "fb",
		Type: contentv1.MediaType_MEDIA_TYPE_VIDEO, Role: contentv1.MediaRole_MEDIA_ROLE_PROFILE_PICTURE,
		Object: &mediav1.StorageObject{Bucket: "b", Key: "k", Sha256: "ABC"}})
	evs, err := d.Decode(topics.ContentMediaReady, b)
	if err != nil || len(evs) != 1 || evs[0].Media == nil || evs[0].Media.Kind != "video" || evs[0].Media.Role != "profile_picture" ||
		!evs[0].Media.IsVideo() || evs[0].Media.Sha256 != "abc" {
		t.Fatalf("ready %v %v", evs, err)
	}
	b, _ = proto.Marshal(&mediav1.MediaReady{MediaId: "fb:media:1"})
	if _, err := d.Decode(topics.ContentMediaReady, b); err == nil {
		t.Fatal("a media announcement without event_id was accepted")
	}
	b, _ = proto.Marshal(&mediav1.MediaDeleted{EventId: "e2", MediaId: "fb:media:1", Reason: mediav1.DeleteReason_DELETE_REASON_EXPIRED})
	evs, err = d.Decode(topics.ContentMediaDeleted, b)
	if err != nil || len(evs) != 1 || evs[0].MediaGone == nil || *evs[0].MediaGone != (core.MediaGone{MediaID: "fb:media:1", Reason: "expired"}) {
		t.Fatalf("deleted %v %v", evs, err)
	}
}

func TestBestCaptions(t *testing.T) {
	tr := func(lang string, src contentv1.CaptionSource, text string) *contentv1.CaptionTrack {
		return &contentv1.CaptionTrack{Language: lang, Source: src, Text: text}
	}
	up, asr, mt := contentv1.CaptionSource_CAPTION_SOURCE_UPLOADER, contentv1.CaptionSource_CAPTION_SOURCE_PLATFORM_ASR,
		contentv1.CaptionSource_CAPTION_SOURCE_MACHINE_TRANSLATION
	for name, c := range map[string]struct {
		tracks []*contentv1.CaptionTrack
		lang   string
		want   string
	}{
		"uploader over asr":        {[]*contentv1.CaptionTrack{tr("ar", asr, "asr"), tr("ar", up, "up")}, "ar", "up"},
		"post's language first":    {[]*contentv1.CaptionTrack{tr("en", up, "en"), tr("ar", up, "ar")}, "ar", "ar"},
		"uploader in another lang": {[]*contentv1.CaptionTrack{tr("ar", asr, "asr-ar"), tr("en", up, "up-en")}, "ar", "up-en"},
		"never a translation":      {[]*contentv1.CaptionTrack{tr("ar", mt, "mt")}, "ar", ""},
		"empty text skipped":       {[]*contentv1.CaptionTrack{tr("ar", up, "  "), tr("ar", asr, "asr")}, "ar", "asr"},
		"none":                     {nil, "ar", ""},
	} {
		got := bestCaptions(c.tracks, c.lang)
		if got.GetText() != c.want {
			t.Errorf("%s: %q, want %q", name, got.GetText(), c.want)
		}
	}
	// The contract's TikTok sample carries the platform's ASR captions.
	var p contentv1.Post
	sample(t, "post.tt.json", &p)
	it := FromContractPost(&p)
	if it.Captions != "example subtitle text" || it.CaptionsLang != "ar" || it.CaptionsSource != "platform_asr" {
		t.Fatalf("sample captions %q %q %q", it.Captions, it.CaptionsLang, it.CaptionsSource)
	}
}
