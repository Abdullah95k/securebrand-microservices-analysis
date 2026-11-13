package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
)

// TestPendingForPostCountsOnlyFilesThatWait: the stats count a pending
// file as waiting for its post as the media worker decides it (13 Nov: the
// count took every file whose owner isn't stored, so TikTok's 8,975 and
// Facebook's 5,205 included files that go ahead without one). A comment's
// file waits only if the comment is in the sample (the same test as
// core.InSample); a page's file and a file past its wait go ahead; a file
// whose post is stored is neither.
func TestPendingForPostCountsOnlyFilesThatWait(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if _, err := s.Ingest(ctx, []*core.Item{comment("tt:comment:stored", "تعليق", at, "a1")}, plan); err != nil {
		t.Fatal(err)
	}
	s.CommentShare = 0.10 // after storing that comment, whatever its ID
	var jobs []*core.MediaJob
	add := func(id, owner string, due time.Duration) {
		j := &core.MediaJob{MediaID: id, OwnerContentID: owner, Platform: "tiktok", Kind: "image", Bucket: "b", Key: id,
			AckFormat: core.AckContract}
		if due > 0 {
			j.DeleteAfter = time.Now().Add(due)
		}
		jobs = append(jobs, j)
	}
	// 300 comments' files, none of the comments stored: only those in the
	// sample wait.
	inSample := 0
	for i := range 300 {
		owner := fmt.Sprintf("tt:comment:%d", i)
		if core.InSample(owner, 0.10) {
			inSample++
		}
		add(fmt.Sprintf("c%d", i), owner, 40*time.Hour)
	}
	if inSample == 0 || inSample == 300 {
		t.Fatalf("%d of 300 in the sample: pick other IDs", inSample)
	}
	add("post", "tt:post:1", 40*time.Hour)           // its post not stored: waits
	add("post-nodeadline", "tt:post:2", 0)           // no deadline: waits
	add("post-late", "tt:post:3", 6*time.Hour)       // within owner_margin of its deadline: goes ahead
	add("page", "tt:account:1", 40*time.Hour)        // an account's picture: no post to wait for
	add("stored", "tt:comment:stored", 40*time.Hour) // its comment stored: neither
	if _, err := s.InsertMediaJobs(ctx, jobs, "sb-analysis"); err != nil {
		t.Fatal(err)
	}
	r, err := s.PlatformCounts(ctx, time.Now().Add(-24*time.Hour), 0, MediaWait{Wait: 168 * time.Hour, Margin: 12 * time.Hour},
		func(string) string { return "tiktok" })
	if err != nil {
		t.Fatal(err)
	}
	var p *PlatformStats
	for i := range r.Platforms {
		if r.Platforms[i].Platform == "tiktok" {
			p = &r.Platforms[i]
		}
	}
	if p == nil {
		t.Fatalf("no tiktok in %+v", r.Platforms)
	}
	wantWait, wantAhead := int64(inSample+2), int64(300-inSample+2)
	if p.PendingForPost != wantWait || p.PendingWithoutPost != wantAhead {
		t.Fatalf("%d waiting for their post, %d going ahead without it; want %d (%d comments in the sample, 2 posts) and %d",
			p.PendingForPost, p.PendingWithoutPost, wantWait, inSample, wantAhead)
	}
	// With no wait configured, nothing waits.
	r, err = s.PlatformCounts(ctx, time.Now().Add(-24*time.Hour), 0, MediaWait{}, func(string) string { return "tiktok" })
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range r.Platforms {
		if q.Platform == "tiktok" && (q.PendingForPost != 0 || q.PendingWithoutPost != wantWait+wantAhead) {
			t.Fatalf("no owner_wait: %d waiting, %d going ahead; want 0 and %d", q.PendingForPost, q.PendingWithoutPost, wantWait+wantAhead)
		}
	}
}
