package store

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
)

// queueComments queues n comments of a platform code, enqueued from
// enqueued on, a second apart, at priority.
func queueComments(t *testing.T, s *Store, code string, n int, enqueued time.Time, priority int) {
	t.Helper()
	ctx := context.Background()
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	var items []*core.Item
	for i := range n {
		items = append(items, comment(fmt.Sprintf("%s:comment:%d", code, i), "تعليق", at, code))
	}
	pl := func(it *core.Item) Plan {
		return Plan{Route: core.RouteComment, Lane: core.LaneBatch, Priority: priority}
	}
	if _, err := s.Ingest(ctx, items, pl); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		if _, err := s.DB.Exec(ctx, `UPDATE analysis.analysis_queue SET enqueued_at = $2 WHERE content_id = $1`,
			fmt.Sprintf("%s:comment:%d", code, i), enqueued.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
}

func claimedByPlatform(cs []Claimed) string {
	n := map[string]int{}
	for _, c := range cs {
		n[core.PlatformCode(c.Item.ContentID)]++
	}
	keys := make([]string, 0, len(n))
	for k := range n {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for _, k := range keys {
		out += fmt.Sprintf("%s=%d ", k, n[k])
	}
	return out
}

// TestClaimSharedAcrossPlatforms: a round's claim is shared across the
// platforms waiting, so a newer platform's items aren't held behind an
// older backlog; a platform with less than its share leaves the rest to
// the others (oldest first).
func TestClaimSharedAcrossPlatforms(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	old := time.Now().Add(-48 * time.Hour)
	queueComments(t, s, "tt", 30, old, core.PriorityNormal)
	queueComments(t, s, "yt", 30, old.Add(time.Hour), core.PriorityNormal)
	queueComments(t, s, "ig", 3, time.Now().Add(-time.Minute), core.PriorityNormal)
	// Oldest first alone: 12 of tt.
	got, err := s.ClaimShared(ctx, core.LaneBatch, core.RouteComment, 12, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// A share of 4 each: ig has 3, so tt and yt take 4 each, and the 1
	// left goes to the oldest (tt).
	if c := claimedByPlatform(got); c != "ig=3 tt=5 yt=4 " || len(got) != 12 {
		t.Fatalf("claimed %s(%d), want ig=3 tt=5 yt=4", c, len(got))
	}
	for _, c := range got {
		if c.Item.Text == "" {
			t.Fatalf("claimed %s without its item", c.Item.ContentID)
		}
	}
}

// TestClaimSharedKeepsBackfillBehindLive: the shares are of live entries:
// a platform with only backfill (background) entries waiting takes none
// until every live entry of every platform is claimed.
func TestClaimSharedKeepsBackfillBehindLive(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	queueComments(t, s, "tt", 20, time.Now().Add(-time.Hour), core.PriorityNormal)
	queueComments(t, s, "fb", 20, time.Now().Add(-48*time.Hour), core.PriorityBackground)
	got, err := s.ClaimShared(ctx, core.LaneBatch, core.RouteComment, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if c := claimedByPlatform(got); c != "tt=10 " {
		t.Fatalf("claimed %s, want tt=10 (backfill after every live entry)", c)
	}
	got, err = s.ClaimShared(ctx, core.LaneBatch, core.RouteComment, 15, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if c := claimedByPlatform(got); c != "fb=5 tt=10 " {
		t.Fatalf("second claim %s, want the last 10 live and then 5 backfill", c)
	}
}

// TestClaimSharedOnePlatform: with one platform waiting it claims as Claim
// does, oldest first.
func TestClaimSharedOnePlatform(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	queueComments(t, s, "tt", 5, time.Now().Add(-time.Hour), core.PriorityNormal)
	got, err := s.ClaimShared(ctx, core.LaneBatch, core.RouteComment, 3, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Item.ContentID != "tt:comment:0" || got[2].Item.ContentID != "tt:comment:2" {
		t.Fatalf("claimed %v, want the 3 oldest", got)
	}
	if codes, err := s.queuePlatforms(ctx, core.LaneBatch, core.RouteComment); err != nil || fmt.Sprint(codes) != "[tt]" {
		t.Fatalf("platforms %v %v", codes, err)
	}
}
