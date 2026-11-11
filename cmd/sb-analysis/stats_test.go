package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/adapter"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/store"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/testenv"
)

// sb-analysis stats: per platform, what came in and what came out, as
// counts. A yt thumbnail is skipped by policy and acknowledged; a yt
// comment waits in the queue.
func TestStatsCountsPerPlatform(t *testing.T) {
	db := testenv.Postgres(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := store.New(db, time.UTC)
	now := time.Now().UTC().Truncate(time.Second)
	items := []*core.Item{
		{ContentID: "yt:comment:1", Platform: "youtube", Kind: core.KindComment, SourceID: "yt:account:1", ThreadID: "yt:post:1",
			ParentID: "yt:post:1", CreatedAt: now, ObservedAt: now, Text: "synthetic", Status: core.StatusVisible, Version: 1},
		{ContentID: "tt:comment:1", Platform: "tiktok", Kind: core.KindComment, SourceID: "tt:account:1", ThreadID: "tt:post:1",
			ParentID: "tt:post:1", CreatedAt: now, ObservedAt: now, Text: "synthetic", Status: core.StatusVisible, Version: 1},
	}
	plan := func(*core.Item) store.Plan {
		return store.Plan{Route: core.RouteComment, Lane: core.LaneBatch, Priority: core.PriorityNormal}
	}
	if _, err := s.Ingest(ctx, items, plan); err != nil {
		t.Fatal(err)
	}
	thumb := &core.MediaJob{MediaID: "yt:media:1", OwnerContentID: "yt:post:1", Platform: "youtube", Kind: "image", Role: "thumbnail",
		Bucket: "b", Key: "k", AckFormat: core.AckContract}
	if _, err := s.InsertMediaJobs(ctx, []*core.MediaJob{thumb}, "sb-analysis"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE analysis.media_jobs SET state = 'skipped', last_error = 'policy:image/thumbnail' WHERE media_id = 'yt:media:1'`); err != nil {
		t.Fatal(err)
	}
	// Two TikTok videos retrying after Whisper couldn't be reached (on two
	// addresses: digits count together), and an image that failed.
	var fails []*core.MediaJob
	for _, id := range []string{"tt:media:1", "tt:media:2", "tt:media:3"} {
		fails = append(fails, &core.MediaJob{MediaID: id, OwnerContentID: "tt:post:1", Platform: "tiktok", Kind: "video", Role: "content",
			Bucket: "b", Key: id, AckFormat: core.AckContract})
	}
	fails[2].Kind = "image"
	if _, err := s.InsertMediaJobs(ctx, fails, "sb-analysis"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE analysis.media_jobs SET tries = 1, last_error = 'transcribe: provider: no server reachable: dial tcp 10.244.3.17:8000' WHERE media_id = 'tt:media:1'`,
		`UPDATE analysis.media_jobs SET tries = 2, last_error = 'transcribe: provider: no server reachable: dial tcp 10.244.9.2:8000' WHERE media_id = 'tt:media:2'`,
		`UPDATE analysis.media_jobs SET state = 'failed', done_at = now(), last_error = 'media: undecodable image' WHERE media_id = 'tt:media:3'`,
	} {
		if _, err := db.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.PlatformCounts(ctx, now.Add(-time.Hour), adapter.PlatformName)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := printStats(&out, r, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"youtube\n  items: 1 ingested, 0 analysed (0 refused), 1 waiting, 0 in dead letters",
		"media: 1 announced, 0 analysed; now 0 pending, 0 done, 1 skipped, 0 failed, 0 gone; 1 acknowledged",
		"skipped because: policy:image/thumbnail 1",
		"tiktok\n  items: 1 ingested",
		"pending now by deadline: 0 past it, 0 within 12 h, 0 in 12-24 h, 0 in 24-48 h, 2 later or none; 2 waiting for their post",
		"2 waiting to be tried again after a failure; failed or retrying because: " +
			"video: transcribe: provider: no server reachable: dial tcp #.#.#.#:# 2, image: media: undecodable image 1",
		"outbox (waiting to be published): empty; 0 held",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
}

// sb-analysis stats: the provider requests by route and lane (how full,
// how many re-sent, tokens and cost per request), and what the real-time
// lane's rules have to work with. Counts only.
func TestStatsRequestsAndUrgentSetup(t *testing.T) {
	db := testenv.Postgres(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	s := store.New(db, time.UTC)
	// Comment route, batch lane: a 3-item request and two 1-item ones
	// done, the second sending c1 again (a retry); one failed; one in
	// flight. A post request in real time. One request from before the
	// window, which isn't counted.
	for _, q := range []string{
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, content_ids, status,
			input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, cost_usd) VALUES
			(gen_random_uuid(), 'comment', 'batch', 'anthropic', 'claude-haiku-5-5', 3, '{c1,c2,c3}', 'done', 600, 450, 7000, 0, 0.0002),
			(gen_random_uuid(), 'comment', 'batch', 'anthropic', 'claude-haiku-5-5', 1, '{c1}', 'done', 200, 150, 7000, 7000, 0.0001),
			(gen_random_uuid(), 'comment', 'batch', 'anthropic', 'claude-haiku-5-5', 1, '{c4}', 'done', 200, 150, 7000, 0, 0.0001),
			(gen_random_uuid(), 'comment', 'batch', 'anthropic', 'claude-haiku-5-5', 2, '{c5,c6}', 'failed', NULL, NULL, NULL, NULL, NULL),
			(gen_random_uuid(), 'comment', 'batch', 'anthropic', 'claude-haiku-5-5', 1, '{c9}', 'failed', NULL, NULL, NULL, NULL, NULL),
			(gen_random_uuid(), 'comment', 'batch', 'anthropic', 'claude-haiku-5-5', 2, '{c7,c8}', 'submitted', NULL, NULL, NULL, NULL, NULL),
			(gen_random_uuid(), 'post', 'realtime', 'anthropic', 'claude-haiku-5-5', 1, '{p1}', 'done', 900, 300, 6000, 0, 0.0004)`,
		// The request in flight: expected at $0.25, $1.50 at worst.
		`UPDATE analysis.provider_requests SET est_cost_usd = 0.25, est_max_usd = 1.5 WHERE status = 'submitted'`,
		`UPDATE analysis.provider_requests SET error = CASE WHEN items = 2
			THEN 'batch request errored: Overloaded (529)' ELSE 'never sent: the process stopped' END WHERE status = 'failed'`,
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, content_ids, status, created_at)
			VALUES (gen_random_uuid(), 'comment', 'batch', 'anthropic', 'claude-haiku-5-5', 1, '{c2}', 'done', now() - interval '2 days')`,
		`INSERT INTO analysis.clients (client_id, name) VALUES ('client-1', 'Client 1'), ('client-2', 'Client 2')`,
		`INSERT INTO analysis.client_sources (client_id, source_id) VALUES ('client-1', 'fb:page:1'), ('client-2', 'fb:page:1'), ('client-2', 'tt:account:2')`,
		`INSERT INTO analysis.entities (entity_id, name, type) VALUES ('brand-a', 'A', 'brand')`,
		`INSERT INTO analysis.client_entities (client_id, entity_id) VALUES ('client-1', 'brand-a'), ('client-2', 'brand-a')`,
		// The comment sample (D-053): yesterday at 1, today at 0.10; a day
		// before the window isn't counted.
		`INSERT INTO analysis.comment_sample_daily (day, platform, received, sampled, urgent, share) VALUES
			((now() AT TIME ZONE 'UTC')::date - 5, 'tiktok', 999, 999, 0, 1),
			((now() AT TIME ZONE 'UTC')::date, 'tiktok', 1000, 98, 2, 0.10)`,
	} {
		if _, err := db.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	since := time.Now().Add(-24 * time.Hour)
	r := store.PlatformReport{HotAccounts: 1, Watchlists: true}
	var err error
	if r.Requests, r.Resent, err = s.RequestCounts(ctx, since); err != nil {
		t.Fatal(err)
	}
	if r.Urgent, err = s.UrgentCounts(ctx); err != nil {
		t.Fatal(err)
	}
	if r.Sample, err = s.CommentSampleCounts(ctx, since); err != nil {
		t.Fatal(err)
	}
	// The queue by tries: two fresh, one back after an error, one after an
	// invalid answer, one on its third try.
	for _, q := range []string{
		`INSERT INTO analysis.analysis_queue (content_id, route, created_at, lane, priority, reason, attempts, last_error) VALUES
			('q1', 'comment', now(), 'batch', 1, 'new', 0, NULL), ('q2', 'comment', now(), 'batch', 1, 'new', 0, NULL),
			('q3', 'comment', now(), 'batch', 1, 'new', 1, 'batch request errored: Overloaded (529)'),
			('q4', 'comment', now(), 'batch', 1, 'new', 1, 'invalid: not JSON'),
			('q5', 'comment', now(), 'batch', 1, 'new', 3, 'no result in batch')`,
	} {
		if _, err := db.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if r.Tries, err = s.QueueTriesNow(ctx); err != nil {
		t.Fatal(err)
	}
	if r.Spend, err = s.SpendByRoute(ctx, since); err != nil {
		t.Fatal(err)
	}
	if r.Measures, err = s.RouteMeasures(ctx, since); err != nil || len(r.Measures) != 0 {
		t.Fatalf("measures %v %v, want none (3 answered)", r.Measures, err)
	}
	r.Measures["post"], r.DefaultOutPerItem = store.RouteMeasure{Requests: 240, OutPerItem: 312, CacheHit: 0.974}, 150
	if r.Backfills, err = s.Backfills(ctx); err != nil || len(r.Backfills) != 0 {
		t.Fatalf("backfills %v %v, want none", r.Backfills, err)
	}
	r.DailyCap, r.RouteCaps = 60, map[string]float64{"comment": 30}
	var out bytes.Buffer
	if err := printStats(&out, r, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"requests, comment route, batch lane: 6 sent (3 done, 2 failed, 1 in flight), 10 items, 1.67 items a request, 3 with one item (50%)",
		"  per done request: 333 input, 7000 cache read, 2333 cache write, 250 output tokens; $0.00 in all, $0.080 per 1,000 items",
		"requests, post route, realtime lane: 1 sent (1 done, 0 failed, 0 in flight), 1 items, 1.00 items a request, 1 with one item (100%)",
		"  failed because (no answer, nothing paid): batch request errored: Overloaded (#) 1, never sent: the process stopped 1",
		"re-sent, comment route: 1 items sent more than once (1 extra sends)",
		"real-time lane rules: 1 hot accounts in the config; 2 accounts monitored by 2 clients; 1 entities tracked",
		"spend today (Iraq day, as the budget counts it): $0.25 of $60.00; worst case $1.50 (every request in flight missing the cache)",
		"  comment: $0.25 of $30.00 (answered $0.00; 1 requests in flight expected at $0.25, $1.50 at worst); worst case $1.50; new requests priced with output at 150 tokens an item and the prompt as a cache miss (the defaults: under 100 answered in the last day)",
		"  post: $0.00 (answered $0.00; 0 requests in flight expected at $0.00, $0.00 at worst); worst case $0.00; new requests priced with output at 312 tokens an item and the prompt 97% read from the cache (measured over the last day's 240 answered)",
		"page backfill (short posts queued before 00011 given their page, for the page top-up): not started yet",
		"queue now, comment route, by tries counted: 2 none, 2 one, 0 two, 1 three or more; 1 last failed on their own answer (sent alone)",
		"  came back because: batch request errored: Overloaded (#) 1, invalid: not JSON 1, no result in batch 1",
		"comments, tiktok (by Iraq day, since the window's first): 1000 received, 98 in the 10% sample (9.8%), 2 kept outside it for the real-time lane; the rest counted, not stored; scale the analysed comments by 10 for all",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "re-sent, post route") {
		t.Errorf("post route shown as re-sent:\n%s", text)
	}
	for _, c := range []struct {
		b    store.Backfill
		want string
	}{
		{store.Backfill{Name: store.BackfillPages, Scanned: 30000, Updated: 1200, LastAt: time.Now()}, "running: 30000 queue entries read, 1200 given their page"},
		{store.Backfill{Name: store.BackfillPages, Scanned: 881234, Updated: 41000, LastAt: time.Now(), FinishedAt: time.Now()}, "done: 881234 queue entries read, 41000 given their page"},
	} {
		r.Backfills = []store.Backfill{c.b}
		out.Reset()
		if err := printStats(&out, r, 24*time.Hour); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "for the page top-up): "+c.want+"\n") {
			t.Errorf("output lacks %q:\n%s", c.want, out.String())
		}
	}
	r.Watchlists = false
	out.Reset()
	if err := printStats(&out, r, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "watchlists not counted (urgent.watchlists off)") {
		t.Errorf("watchlists off not said:\n%s", out.String())
	}
}
