package eval

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/entities"
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
			exec(`INSERT INTO comments (post_created_at, post_id, comment_id, page_id, depth, text, content_hash, first_seen_at, last_seen_at, author_id)
				VALUES ($1, $2, $3, $4, 0, $5, '\x00', now(), now(), 'secret-author')`, at, postID, postID*10+int64(i), page, txt)
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
