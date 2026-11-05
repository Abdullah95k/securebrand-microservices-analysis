package eval

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/entities"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/textnorm"
	"github.com/abdullah95k/securebrand-microservices/internal/store"
	"github.com/abdullah95k/securebrand-microservices/internal/testenv"
)

func TestSampleFromPilotTables(t *testing.T) {
	db := testenv.Postgres(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	if _, err := store.EnsurePartitions(ctx, db, at, store.PartitionWindow{MonthsBack: 1, MonthsAhead: 1}); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	texts := []string{"اسياسيل النت واقف", "حلو", "شكد السعر؟ " + "كلام طويل شوية حتى يصير متوسط الطول", "سڵاو چۆنی", "shlonak",
		"ok", "تمام", "كلام طويل جدا جدا عن الكهرباء والماء والخدمات في المحافظة والوضع العام وما يجري كل يوم بدون حلول حقيقية من الجهات"}
	for page := int64(1); page <= 3; page++ {
		exec(`INSERT INTO pages (page_id, name) VALUES ($1, $2)`, page, fmt.Sprintf("Page %d", page))
		postID := page * 100
		exec(`INSERT INTO posts (post_created_at, post_id, page_id, post_type, text, content_hash, first_seen_at, last_seen_at)
			VALUES ($1, $2, $3, 'photo', 'خبر عن الكهرباء في بغداد اليوم', '\x00', now(), now())`, at, postID, page)
		exec(`INSERT INTO post_index (post_id, page_id, post_created_at) VALUES ($1, $2, $3)`, postID, page, at)
		for i, txt := range texts {
			// Every comment ends by tagging a person (fbs's text_ranges,
			// UTF-16 offsets): the gold set must never hold the name.
			tagged := txt + " Zed Zed"
			ranges := fmt.Sprintf(`[{"offset":%d,"length":7,"entity_type":"User","name":"Zed Zed"}]`, len(utf16.Encode([]rune(txt+" "))))
			exec(`INSERT INTO comments (post_created_at, post_id, comment_id, page_id, depth, text, text_ranges, content_hash, first_seen_at, last_seen_at, author_id)
				VALUES ($1, $2, $3, $4, 0, $5, $6, '\x00', now(), now(), 'secret-author')`, at, postID, postID*10+int64(i), page, tagged, ranges)
		}
	}
	seed, _ := entities.LoadSeed("")
	var ents []entities.Entity
	var aliases []entities.Alias
	for _, e := range seed.Entities {
		ents = append(ents, entities.Entity{ID: e.ID, Name: e.Name, Type: e.Type})
		for _, a := range e.Aliases {
			aliases = append(aliases, entities.Alias{Alias: a, EntityID: e.ID})
		}
	}
	items, err := Sample(ctx, db, entities.New(ents, aliases, nil), SampleConfig{Comments: 12, Posts: 2, BrandShare: 0.25, ScriptShare: 0.25, Seed: 3})
	if err != nil {
		t.Fatal(err)
	}
	var comments, posts, brand, script int
	pages := map[string]bool{}
	for _, it := range items {
		if strings.Contains(it.Text, "Zed") || !strings.HasPrefix(it.Kind, "post") && !strings.HasSuffix(it.Text, textnorm.TagPlaceholder) {
			t.Fatalf("tagged name not hidden: %q", it.Text)
		}
		if it.ID == "" || it.Label != nil || it.Text == "" {
			t.Fatalf("bad item %+v", it)
		}
		switch it.Kind {
		case "comment":
			comments++
			pages[it.Page] = true
			if it.Post == "" {
				t.Fatal("comment without post context")
			}
		case "post":
			posts++
		}
		for _, s := range it.Strata {
			brand += map[bool]int{true: 1}[s == "brand"]
			script += map[bool]int{true: 1}[s == "script"]
		}
	}
	if comments != 12 || posts != 2 || brand != 3 || script != 3 || len(pages) != 3 {
		t.Fatalf("comments %d posts %d brand %d script %d pages %d", comments, posts, brand, script, len(pages))
	}
	again, _ := Sample(ctx, db, entities.New(ents, aliases, nil), SampleConfig{Comments: 12, Posts: 2, BrandShare: 0.25, ScriptShare: 0.25, Seed: 3})
	for i := range items {
		if items[i].ID != again[i].ID {
			t.Fatal("sampling must be reproducible for a seed")
		}
	}
}

// Tagged people in the pilot's texts are written as "@user" in the gold
// set, from fbs's stored ranges (UTF-16 offsets); tagged registry entities
// and unreadable ranges leave the text as it is.
func TestUntag(t *testing.T) {
	reg := entities.New([]entities.Entity{{ID: "examplebrand", Name: "Example Brand", Type: "brand"}}, nil, nil)
	text := "😀 Sara Ahmed and Example Brand"
	ranges := `[{"offset":3,"length":10,"entity_type":"User","name":"Sara Ahmed"},
		{"offset":18,"length":13,"entity_type":"User","name":"Example Brand"}]`
	if got := untag(text, []byte(ranges), reg); got != "😀 @user and Example Brand" {
		t.Fatalf("got %q", got)
	}
	if got := untag(text, []byte("not json"), reg); got != text {
		t.Fatalf("bad ranges changed the text: %q", got)
	}
	if got := untag(text, nil, reg); got != text {
		t.Fatalf("no ranges changed the text: %q", got)
	}
}
