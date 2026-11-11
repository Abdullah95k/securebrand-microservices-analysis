package store

import (
	"context"
	"testing"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
)

// TestOlderReadingIsIgnored: fbs's envelope.version is the reading's time
// and only rises per item. A contract copy at or below the stored version
// (a redelivery, or a backfill of an older scrape such as the pilot's)
// changes nothing and isn't queued or counted in the sample; a newer one
// with other text is an edit as usual; an item stored from fbs's own topics
// (version 0) isn't compared.
func TestOlderReadingIsIgnored(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	reading := func(version int64, text string) *core.Item {
		c := comment("fb:comment:9", text, at, "a")
		c.Version, c.ObservedAt = version, at
		return c
	}
	if _, err := s.Ingest(ctx, []*core.Item{reading(1_731_300_000_000_000, "الخدمة ممتازة اليوم")}, plan); err != nil {
		t.Fatal(err)
	}
	// Analysed meanwhile: off the queue.
	if _, err := s.DB.Exec(ctx, `DELETE FROM analysis.analysis_queue`); err != nil {
		t.Fatal(err)
	}
	received := func() int {
		return count(t, s, `SELECT coalesce(sum(received), 0)::int FROM analysis.comment_sample_daily`)
	}
	before := received()
	for _, c := range []struct {
		name    string
		version int64
		text    string
	}{
		{"an older reading with other text", 1_730_500_000_000_000, "الخدمة سيئة"},
		{"the same reading again", 1_731_300_000_000_000, "الخدمة ممتازة اليوم"},
	} {
		st, err := s.Ingest(ctx, []*core.Item{reading(c.version, c.text)}, plan)
		if err != nil {
			t.Fatal(err)
		}
		if st.Stale != 1 || st.Edited != 0 || st.Unchanged != 0 || st.New != 0 {
			t.Fatalf("%s: stats %+v, want it stale", c.name, st)
		}
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.content_items WHERE content_id = 'fb:comment:9' AND text = 'الخدمة ممتازة اليوم'`); n != 1 {
		t.Fatal("an older reading replaced the stored text")
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.analysis_queue`); n != 0 {
		t.Fatal("a stale copy was queued")
	}
	if received() != before {
		t.Fatal("a stale copy was counted in the comment sample")
	}
	// A newer reading with other text: an edit, queued again.
	st, err := s.Ingest(ctx, []*core.Item{reading(1_731_400_000_000_000, "الخدمة سيئة الآن")}, plan)
	if err != nil {
		t.Fatal(err)
	}
	if st.Edited != 1 || st.Stale != 0 {
		t.Fatalf("newer reading: stats %+v, want an edit", st)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.content_items WHERE content_id = 'fb:comment:9' AND content_version = 1731400000000000`); n != 1 {
		t.Fatal("the newer reading's version wasn't stored")
	}
	// An item from fbs's own topics (version 0) has nothing to compare:
	// its contract copy goes on as before (here: unchanged, taking the
	// contract's version).
	fb := comment("fb:comment:10", "تعليق من المسار القديم", at, "b")
	if _, err := s.Ingest(ctx, []*core.Item{fb}, plan); err != nil {
		t.Fatal(err)
	}
	contract := *fb
	contract.Version = 1_730_500_000_000_000
	if st, err := s.Ingest(ctx, []*core.Item{&contract}, plan); err != nil || st.Unchanged != 1 || st.Stale != 0 {
		t.Fatalf("contract copy of an fb.* item: stats %+v, %v", st, err)
	}
}

// TestOlderReadingKeepsDeletionsAndErasure: the version rule is for
// readings of the text: a deletion still marks the item deleted, whatever
// its version.
func TestOlderReadingKeepsDeletions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	c := comment("fb:comment:11", "تعليق", at, "a")
	c.Version = 1_731_300_000_000_000
	if _, err := s.Ingest(ctx, []*core.Item{c}, plan); err != nil {
		t.Fatal(err)
	}
	del := *c
	del.Status, del.Version = core.StatusDeleted, 1_731_300_000_000_000
	if st, err := s.Ingest(ctx, []*core.Item{&del}, plan); err != nil || st.Deleted != 1 {
		t.Fatalf("deletion: stats %+v, %v", st, err)
	}
}
