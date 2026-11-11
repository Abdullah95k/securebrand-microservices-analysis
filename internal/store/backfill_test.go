package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
)

// TestQueueCarriesShortPostsPage: a short post's queue entry (a post on the
// comment route) carries its page; comments and posts on the post route
// don't.
func TestQueueCarriesShortPostsPage(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour)
	short := &core.Item{ContentID: "fb:post:1", Platform: "facebook", Kind: core.KindPost, SourceID: "fb:page:1", CreatedAt: at,
		ObservedAt: at, Text: "منشور قصير", Status: core.StatusVisible}
	long := &core.Item{ContentID: "fb:post:2", Platform: "facebook", Kind: core.KindPost, SourceID: "fb:page:1", CreatedAt: at,
		ObservedAt: at, Text: "منشور طويل", Status: core.StatusVisible}
	byRoute := func(it *core.Item) Plan {
		p := plan(it)
		if it.ContentID == "fb:post:2" {
			p.Route = core.RoutePost
		}
		return p
	}
	if _, err := s.Ingest(ctx, []*core.Item{short, long, comment("fb:comment:1", "تعليق", at, "a")}, byRoute); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	rows, err := s.DB.Query(ctx, `SELECT content_id, coalesce(source_id, '-') FROM analysis.analysis_queue`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, src string
		if err := rows.Scan(&id, &src); err != nil {
			t.Fatal(err)
		}
		got[id] = src
	}
	rows.Close()
	if fmt.Sprint(got) != "map[fb:comment:1:- fb:post:1:fb:page:1 fb:post:2:-]" {
		t.Fatalf("queue pages %v", got)
	}
}

// TestBackfillPages: entries queued before 00011 (no page) get theirs,
// pass by pass in content-ID order; only short posts change; the pass that
// reads fewer than its rows finishes the backfill, and later passes do
// nothing.
func TestBackfillPages(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour)
	var items []*core.Item
	for i := range 4 {
		items = append(items, &core.Item{ContentID: fmt.Sprintf("fb:post:%d", i), Platform: "facebook", Kind: core.KindPost,
			SourceID: fmt.Sprintf("fb:page:%d", i%2), CreatedAt: at, ObservedAt: at, Text: "منشور", Status: core.StatusVisible})
	}
	for i := range 3 {
		items = append(items, comment(fmt.Sprintf("fb:comment:%d", i), "تعليق", at, "a"))
	}
	if _, err := s.Ingest(ctx, items, plan); err != nil {
		t.Fatal(err)
	}
	// As queued before the column: no pages.
	if _, err := s.DB.Exec(ctx, `UPDATE analysis.analysis_queue SET source_id = NULL`); err != nil {
		t.Fatal(err)
	}
	if b, err := s.Backfills(ctx); err != nil || len(b) != 0 {
		t.Fatalf("backfills before the first pass %+v %v", b, err)
	}
	passes := 0
	for {
		done, err := s.BackfillPagesPass(ctx, 3)
		if err != nil {
			t.Fatal(err)
		}
		passes++
		if done || passes > 10 {
			break
		}
	}
	// 7 entries in passes of 3: 3, 3, 1 (the last finishes it).
	if passes != 3 {
		t.Fatalf("%d passes, want 3", passes)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.analysis_queue q JOIN analysis.content_items c USING (content_id)
		WHERE q.source_id = c.source_id AND c.kind = 'post'`); n != 4 {
		t.Fatalf("%d of 4 posts given their page", n)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.analysis_queue WHERE source_id IS NOT NULL AND content_id LIKE 'fb:comment:%'`); n != 0 {
		t.Fatalf("%d comments given a page", n)
	}
	b, err := s.Backfills(ctx)
	if err != nil || len(b) != 1 || b[0].Name != BackfillPages || b[0].Scanned != 7 || b[0].Updated != 4 || b[0].FinishedAt.IsZero() || b[0].LastAt.IsZero() {
		t.Fatalf("progress %+v %v, want 7 read, 4 given their page, finished", b, err)
	}
	// Done: another pass reads nothing more.
	if done, err := s.BackfillPagesPass(ctx, 3); err != nil || !done {
		t.Fatalf("pass after the end: done %v, %v", done, err)
	}
	if b, _ := s.Backfills(ctx); b[0].Scanned != 7 {
		t.Fatalf("a pass after the end read again: %+v", b[0])
	}
}

// TestRunBackfillPagesWaitsWhenDone: the loop runs passes until the end,
// then waits for its context instead of returning, as a role's job.
func TestRunBackfillPagesWaitsWhenDone(t *testing.T) {
	s := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- s.RunBackfillPages(ctx, 10, time.Millisecond) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, err := s.Backfills(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(b) == 1 && !b[0].FinishedAt.IsZero() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the backfill didn't finish on an empty queue")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case err := <-errc:
		t.Fatalf("the loop returned when done (%v); it should wait for its context", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}
