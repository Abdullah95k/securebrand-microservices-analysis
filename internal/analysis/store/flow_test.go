package store

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/entities"
)

func plan(it *core.Item) Plan {
	return Plan{Route: core.RouteComment, Lane: core.LaneBatch, Priority: core.PriorityNormal}
}

func comment(id, text string, at time.Time, author string) *core.Item {
	return &core.Item{ContentID: id, Platform: "facebook", Kind: core.KindComment, SourceID: "fb:page:1", ThreadID: "fb:post:1",
		ParentID: "fb:post:1", AuthorKey: []byte(author), CreatedAt: at, ObservedAt: at, Text: text, Status: core.StatusVisible}
}

func count(t *testing.T, s *Store, q string, args ...any) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func TestIngestIdempotentEditsDeletionsSkips(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 30, 21, 30, 0, 0, time.UTC) // 2026-10-31 00:30 in Baghdad
	items := []*core.Item{
		comment("fb:comment:1", "شبكة اسياسيل خايسة", at, "a1"),
		comment("fb:comment:2", "", at, "a2"), // nothing to analyse
		{ContentID: "fb:post:1", Platform: "facebook", Kind: core.KindPost, SourceID: "fb:page:1", SourceName: "Asiacell",
			CreatedAt: at.Add(-time.Hour), ObservedAt: at, Text: "عرض جديد", Status: core.StatusVisible},
	}
	st, err := s.Ingest(ctx, items, plan)
	if err != nil {
		t.Fatal(err)
	}
	if st.New != 3 || st.Skipped != 1 {
		t.Fatalf("first ingest: %+v", st)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.analysis_queue`); n != 2 {
		t.Fatalf("queued %d, want 2", n)
	}
	// Same content again (a re-scrape): nothing changes.
	st, err = s.Ingest(ctx, []*core.Item{comment("fb:comment:1", "شبكة اسياسيل خايسة", at, "a1")}, plan)
	if err != nil || st.Unchanged != 1 {
		t.Fatalf("re-scrape: %+v %v", st, err)
	}
	// An edit updates the text and re-queues.
	if _, err := s.DB.Exec(ctx, `DELETE FROM analysis.analysis_queue`); err != nil {
		t.Fatal(err)
	}
	st, err = s.Ingest(ctx, []*core.Item{comment("fb:comment:1", "شبكة اسياسيل تمام", at, "a1")}, plan)
	if err != nil || st.Edited != 1 {
		t.Fatalf("edit: %+v %v", st, err)
	}
	if n := count(t, s, `SELECT edit_count FROM analysis.content_items WHERE content_id = 'fb:comment:1'`); n != 1 {
		t.Fatalf("edit_count %d", n)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.analysis_queue WHERE reason = 'edit'`); n != 1 {
		t.Fatal("edit not queued")
	}
	// A deletion keeps the text; a later sighting restores it.
	del := comment("fb:comment:1", "", at, "a1")
	del.Status = core.StatusDeleted
	if st, err = s.Ingest(ctx, []*core.Item{del}, plan); err != nil || st.Deleted != 1 {
		t.Fatalf("delete: %+v %v", st, err)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.content_items WHERE status = 'deleted' AND text IS NOT NULL`); n != 1 {
		t.Fatal("deleted item lost its text")
	}
	if _, err = s.Ingest(ctx, []*core.Item{comment("fb:comment:1", "شبكة اسياسيل تمام", at, "a1")}, plan); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.content_items WHERE status = 'visible' AND content_id = 'fb:comment:1'`); n != 1 {
		t.Fatal("not visible again")
	}
}

func TestClaimWriteResultsAndStaleness(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if _, err := s.Ingest(ctx, []*core.Item{comment("fb:comment:1", "حلو", at, "a1"), comment("fb:comment:2", "زين", at, "a2")}, plan); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.Claim(ctx, core.LaneBatch, core.RouteComment, 10, time.Hour, 0)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim: %d %v", len(claimed), err)
	}
	if again, _ := s.Claim(ctx, core.LaneBatch, core.RouteComment, 10, time.Hour, 0); len(again) != 0 {
		t.Fatal("leased items claimed twice")
	}
	req := uuid.New()
	var cids []string
	var hashes [][]byte
	for _, c := range claimed {
		cids, hashes = append(cids, c.Item.ContentID), append(hashes, c.Item.InputHash())
	}
	if err := s.StartRequest(ctx, RequestRow{ID: req, Route: core.RouteComment, Lane: core.LaneBatch, Provider: "fake", Model: "m",
		ContentIDs: cids, InputHashes: hashes, EstCostUSD: 0.01}); err != nil {
		t.Fatal(err)
	}
	// Item 2 is edited while the request is in flight: its result is stale.
	if _, err := s.Ingest(ctx, []*core.Item{comment("fb:comment:2", "زين جدا", at, "a2")}, plan); err != nil {
		t.Fatal(err)
	}
	var results []ItemResult
	for i, c := range claimed {
		results = append(results, ItemResult{Item: c.Item, Route: core.RouteComment, InputHash: hashes[i],
			Result: core.Result{Sentiment: "positive", Lang: "ar", Topics: []string{"telecom.network"},
				Entities: []core.Entity{{ID: "asiacell", Name: "Asiacell", Type: "brand", Sentiment: "positive", Aspect: "network"}}},
			Prov: core.Provenance{Provider: "fake", Model: "m", PromptVersion: "p1", TaxonomyVersion: "t", SchemaVersion: "s1",
				Lane: core.LaneBatch, AnalyzedAt: time.Now()}})
	}
	ws, err := s.WriteResults(ctx, results, RequestOutcome{ID: req, CostUSD: 0.002, Usage: [4]int{100, 50, 0, 0}})
	if err != nil {
		t.Fatal(err)
	}
	if ws.Written != 1 || ws.Stale != 1 {
		t.Fatalf("write stats %+v", ws)
	}
	// The edited item stays queued (its new text needs analysis).
	if n := count(t, s, `SELECT count(*) FROM analysis.analysis_queue WHERE content_id = 'fb:comment:2'`); n != 1 {
		t.Fatal("edited item lost its queue entry")
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.analysis_queue WHERE content_id = 'fb:comment:1'`); n != 0 {
		t.Fatal("analysed item still queued")
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.entity_mentions WHERE entity_id = 'asiacell'`); n != 1 {
		t.Fatal("mention not stored")
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.outbox`); n != 1 {
		t.Fatal("no outbox record")
	}
	// Writing the same result again (a re-polled batch) is a duplicate.
	ws, err = s.WriteResults(ctx, results[:1], RequestOutcome{})
	if err != nil || ws.Duplicate != 1 {
		t.Fatalf("duplicate: %+v %v", ws, err)
	}
	// Spend is recorded for the budget.
	spent, err := s.SpentSince(ctx, time.Now().Add(-time.Hour), "")
	if err != nil || spent < 0.0019 || spent > 0.0021 {
		t.Fatalf("spent %v %v", spent, err)
	}
	// Rollups of the dirty day.
	if n, err := s.RecomputeDirtyDays(ctx, 10, 0); err != nil || n != 1 {
		t.Fatalf("rollups: %d %v", n, err)
	}
	if n := count(t, s, `SELECT mentions FROM analysis.rollup_entity_daily WHERE entity_id = 'asiacell'`); n != 1 {
		t.Fatal("entity rollup")
	}
	if n := count(t, s, `SELECT mentions FROM analysis.rollup_entity_topic_daily WHERE entity_id = 'asiacell' AND topic = 'telecom.network'`); n != 1 {
		t.Fatal("entity topic rollup")
	}
	if n := count(t, s, `SELECT positive FROM analysis.rollup_source_daily WHERE source_id = 'fb:page:1'`); n != 1 {
		t.Fatal("source rollup")
	}
}

func TestFailuresBackoffAndDeadLetters(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour)
	if _, err := s.Ingest(ctx, []*core.Item{comment("fb:comment:9", "ليش؟", at, "a9")}, plan); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if _, err := s.DB.Exec(ctx, `UPDATE analysis.analysis_queue SET not_before = now() - interval '1s'`); err != nil {
			t.Fatal(err)
		}
		c, err := s.Claim(ctx, core.LaneBatch, core.RouteComment, 10, time.Hour, 0)
		if err != nil || len(c) != 1 || c[0].Attempts != attempt {
			t.Fatalf("attempt %d: %+v %v", attempt, c, err)
		}
		id := uuid.New()
		if err := s.StartRequest(ctx, RequestRow{ID: id, Route: core.RouteComment, Lane: core.LaneBatch, Provider: "p", Model: "m",
			ContentIDs: []string{"fb:comment:9"}, InputHashes: [][]byte{{}}}); err != nil {
			t.Fatal(err)
		}
		if err := s.FailRequest(ctx, id, "boom", 3); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.dead_letters WHERE content_id = 'fb:comment:9'`); n != 1 {
		t.Fatal("not dead-lettered after max attempts")
	}
	if n, err := s.ReplayFailed(ctx, "", 10); err != nil || n != 1 {
		t.Fatalf("replay: %d %v", n, err)
	}
	// An unsent claim (budget) does not count an attempt.
	c, _ := s.Claim(ctx, core.LaneBatch, core.RouteComment, 10, time.Hour, 0)
	if len(c) != 1 {
		t.Fatal("replayed item not claimable")
	}
	if err := s.ReleaseUnsent(ctx, []string{"fb:comment:9"}, core.RouteComment, 0); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT attempts FROM analysis.analysis_queue WHERE content_id = 'fb:comment:9'`); n != 0 {
		t.Fatalf("attempts after release = %d", n)
	}
}

func TestEraseAuthorAndSource(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-2 * time.Hour)
	a := comment("fb:comment:1", "اسياسيل زين", at, "author-x")
	b := comment("fb:comment:2", "اسياسيل سيء", at, "author-y")
	if _, err := s.Ingest(ctx, []*core.Item{a, b}, plan); err != nil {
		t.Fatal(err)
	}
	for _, it := range []*core.Item{a, b} {
		if _, err := s.WriteResults(ctx, []ItemResult{{Item: *it, Route: core.RouteComment, InputHash: it.InputHash(),
			Result: core.Result{Sentiment: "neutral", Entities: []core.Entity{{ID: "asiacell", Name: "Asiacell", Type: "brand", Sentiment: "neutral"}}},
			Prov:   core.Provenance{Provider: "p", Model: "m", AnalyzedAt: time.Now()}}}, RequestOutcome{}); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := s.Erase(ctx, core.Erasure{RequestID: "r1", Subject: "author", AuthorKey: []byte("author-x")}, "analysis")
	if err != nil {
		t.Fatal(err)
	}
	if stats["mentions"] != 1 || stats["analyses"] != 1 {
		t.Fatalf("stats %v", stats)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.content_items WHERE author_key IS NULL AND text IS NULL AND status = 'redacted'`); n != 1 {
		t.Fatal("author's item not redacted")
	}
	// Idempotent; and the author's re-scraped comment is suppressed.
	if st, _ := s.Erase(ctx, core.Erasure{RequestID: "r1", Subject: "author", AuthorKey: []byte("author-x")}, "analysis"); st["already_done"] != 1 {
		t.Fatal("erasure repeated")
	}
	st, err := s.Ingest(ctx, []*core.Item{comment("fb:comment:3", "نص جديد", at, "author-x")}, plan)
	if err != nil || st.Suppressed != 1 {
		t.Fatalf("suppression: %+v %v", st, err)
	}
	if n := count(t, s, `SELECT count(*) FROM analysis.erased_subjects WHERE subject_key = $1`, hex.EncodeToString([]byte("author-x"))); n != 1 {
		t.Fatal("subject not recorded")
	}
	// Source erasure removes the rest.
	if _, err := s.Erase(ctx, core.Erasure{RequestID: "r2", Subject: "source", SourceID: "fb:page:1"}, "analysis"); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"content_items", "analyses", "entity_mentions", "content_index", "analysis_queue"} {
		if n := count(t, s, `SELECT count(*) FROM analysis.`+tbl); n != 0 {
			t.Fatalf("%s: %d rows left", tbl, n)
		}
	}
}

func TestRegistryAndCandidates(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	seed, err := entities.LoadSeed("")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.SeedIfEmpty(ctx, seed); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	if ok, _ := s.SeedIfEmpty(ctx, seed); ok {
		t.Fatal("seeded twice")
	}
	reg, err := s.LoadRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m := reg.Find("والله شبكة آسيا سيل اليوم واقفة"); len(m) != 1 || m[0].EntityID != "asiacell" || m[0].Tracked {
		t.Fatalf("find: %+v", m)
	}
	if err := s.AddCandidates(ctx, []Candidate{{Surface: "شركة نجمة", Type: "brand", ContentID: "c1"},
		{Surface: "شركة نجمة", Type: "brand", ContentID: "c2"}, {Surface: "Ali", Type: "person_private", ContentID: "c3"}}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.PendingCandidates(ctx, 2, 10)
	if err != nil || len(rows) != 1 || rows[0].Seen != 2 {
		t.Fatalf("candidates %+v %v", rows, err)
	}
	if err := s.ResolveCandidate(ctx, rows[0].Norm, "brand", "accept", "najma", "Najma"); err != nil {
		t.Fatal(err)
	}
	if err := s.TrackEntity(ctx, "c1", "asiacell", "own"); err == nil {
		t.Fatal("tracking for an unknown client should fail")
	}
	if _, err := s.DB.Exec(ctx, `INSERT INTO analysis.clients (client_id, name) VALUES ('c1', 'Client')`); err != nil {
		t.Fatal(err)
	}
	if err := s.TrackEntity(ctx, "c1", "asiacell", "own"); err != nil {
		t.Fatal(err)
	}
	reg, _ = s.LoadRegistry(ctx)
	if e, ok := reg.Get("asiacell"); !ok || !e.Tracked {
		t.Fatal("tracked flag")
	}
	if e, ok := reg.Get("najma"); !ok || e.Name != "Najma" {
		t.Fatal("accepted candidate missing")
	}
	if err := s.MergeEntity(ctx, "najma", "asiacell"); err != nil {
		t.Fatal(err)
	}
	reg, _ = s.LoadRegistry(ctx)
	if e, ok := reg.Get("najma"); !ok || e.ID != "asiacell" {
		t.Fatal("merge not followed")
	}
	if !bytes.Equal([]byte(reg.Link("", "شركة نجمة")), []byte("asiacell")) {
		t.Fatalf("alias moved: %q", reg.Link("", "شركة نجمة"))
	}
}
