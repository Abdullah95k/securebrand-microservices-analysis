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
	plan := func(*core.Item) store.Plan { return store.Plan{Route: core.RouteComment, Lane: core.LaneBatch, Priority: core.PriorityNormal} }
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
		"outbox (waiting to be published): empty; 0 held",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}
}
