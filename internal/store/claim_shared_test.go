package store

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
)

// queueComments queues n comments of a platform code, published an hour
// ago, enqueued from enqueued on, a second apart, at priority.
func queueComments(t *testing.T, s *Store, code string, n int, enqueued time.Time, priority int) {
	t.Helper()
	queueCommentsAt(t, s, code, 0, n, time.Now().Add(-time.Hour), enqueued, priority)
}

// queueCommentsAt queues comments from..from+n-1 of a platform code,
// published at published, enqueued from enqueued on, a second apart, at
// priority.
func queueCommentsAt(t *testing.T, s *Store, code string, from, n int, published, enqueued time.Time, priority int) {
	t.Helper()
	ctx := context.Background()
	at := published.UTC().Truncate(time.Second)
	var items []*core.Item
	for i := range n {
		items = append(items, comment(fmt.Sprintf("%s:comment:%d", code, from+i), "تعليق", at, code))
	}
	pl := func(it *core.Item) Plan {
		return Plan{Route: core.RouteComment, Lane: core.LaneBatch, Priority: priority}
	}
	if _, err := s.Ingest(ctx, items, pl); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		if _, err := s.DB.Exec(ctx, `UPDATE analysis.analysis_queue SET enqueued_at = $2 WHERE content_id = $1`,
			fmt.Sprintf("%s:comment:%d", code, from+i), enqueued.Add(time.Duration(i)*time.Second)); err != nil {
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
	got, err := s.ClaimShared(ctx, core.LaneBatch, core.RouteComment, 12, time.Minute, 0)
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
	got, err := s.ClaimShared(ctx, core.LaneBatch, core.RouteComment, 10, time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	if c := claimedByPlatform(got); c != "tt=10 " {
		t.Fatalf("claimed %s, want tt=10 (backfill after every live entry)", c)
	}
	got, err = s.ClaimShared(ctx, core.LaneBatch, core.RouteComment, 15, time.Minute, 0)
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
	got, err := s.ClaimShared(ctx, core.LaneBatch, core.RouteComment, 3, time.Minute, 0)
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

// TestPlatformCountsWaitingSplit: each platform's waiting items say how
// many are in a request in flight and how many are backfill, and how long
// the oldest not in flight has waited.
func TestPlatformCountsWaitingSplit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	queueComments(t, s, "ig", 3, time.Now().Add(-2*time.Hour), core.PriorityNormal)
	queueComments(t, s, "fb", 2, time.Now().Add(-5*time.Hour), core.PriorityBackground)
	// ig's oldest is in a request in flight; its next waits 2 h less a second.
	if _, err := s.DB.Exec(ctx, `UPDATE analysis.analysis_queue SET request_id = gen_random_uuid() WHERE content_id = 'ig:comment:0'`); err != nil {
		t.Fatal(err)
	}
	r, err := s.PlatformCounts(ctx, time.Now().Add(-24*time.Hour), 0, MediaWait{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]PlatformStats{}
	for _, p := range r.Platforms {
		got[p.Platform] = p
	}
	ig, fb := got["ig"], got["fb"]
	if ig.Waiting != 3 || ig.InFlight != 1 || ig.Backfill != 0 || ig.OldestWaiting < 119*time.Minute || ig.OldestWaiting > 121*time.Minute {
		t.Fatalf("ig %+v, want 3 waiting, 1 in flight, the oldest not sent about 2 h", ig)
	}
	if fb.Waiting != 2 || fb.InFlight != 0 || fb.Backfill != 2 {
		t.Fatalf("fb %+v, want 2 waiting, both backfill", fb)
	}
}

// TestClaimSharedRecentFirst (CLIENT-REQ-018): live items published within
// the window are claimed before older ones, within each platform's share
// and then across platforms for what is left; backfill entries still come
// after every live one, however recent; and the older backlog is claimed,
// oldest first, once no recent item waits.
func TestClaimSharedRecentFirst(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now()
	old := now.Add(-10 * 24 * time.Hour)
	// fb: 3 published 10 days ago, queued then; 5 published an hour ago,
	// queued since. ig: one recent live comment, one recent backfill one.
	queueCommentsAt(t, s, "fb", 0, 3, old, old, core.PriorityNormal)
	queueCommentsAt(t, s, "fb", 3, 5, now.Add(-time.Hour), now.Add(-50*time.Minute), core.PriorityNormal)
	queueCommentsAt(t, s, "ig", 0, 1, now.Add(-time.Hour), now.Add(-40*time.Minute), core.PriorityNormal)
	queueCommentsAt(t, s, "ig", 1, 1, now.Add(-time.Hour), now.Add(-40*time.Minute), core.PriorityBackground)

	// Shares of 3: fb's 3 recent, ig's 1 recent live; ig has no older live
	// one; the 2 left go to fb's next recent ones, not its older ones.
	got, err := s.ClaimShared(ctx, core.LaneBatch, core.RouteComment, 6, time.Minute, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, c := range got {
		ids[c.Item.ContentID] = true
	}
	want := []string{"fb:comment:3", "fb:comment:4", "fb:comment:5", "fb:comment:6", "fb:comment:7", "ig:comment:0"}
	if len(got) != len(want) {
		t.Fatalf("claimed %d (%s), want the 6 recent live ones", len(got), claimedByPlatform(got))
	}
	for _, id := range want {
		if !ids[id] {
			t.Fatalf("%s not claimed; claimed %v", id, ids)
		}
	}
	for _, c := range got {
		if c.Item.ContentID == "" || c.Item.Text == "" {
			t.Fatalf("claimed entry without its item: %+v", c.Item)
		}
	}

	// No recent live item waits: the older backlog, oldest first, then the
	// backfill entry.
	got, err = s.ClaimShared(ctx, core.LaneBatch, core.RouteComment, 10, time.Minute, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, c := range got {
		order = append(order, c.Item.ContentID)
	}
	if fmt.Sprint(order) != "[fb:comment:0 fb:comment:1 fb:comment:2 ig:comment:1]" {
		t.Fatalf("then claimed %v, want fb's 3 older ones oldest first, then ig's backfill", order)
	}

	// Off (0): the oldest first, as before.
	s2 := testStore(t)
	queueCommentsAt(t, s2, "fb", 0, 3, old, old, core.PriorityNormal)
	queueCommentsAt(t, s2, "fb", 3, 3, now.Add(-time.Hour), now.Add(-50*time.Minute), core.PriorityNormal)
	got, err = s2.ClaimShared(ctx, core.LaneBatch, core.RouteComment, 3, time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	order = nil
	for _, c := range got {
		order = append(order, c.Item.ContentID)
	}
	if fmt.Sprint(order) != "[fb:comment:0 fb:comment:1 fb:comment:2]" {
		t.Fatalf("with recent_first off claimed %v, want the 3 oldest", order)
	}

	// One platform: its recent ones, then its oldest.
	s3 := testStore(t)
	queueCommentsAt(t, s3, "fb", 0, 3, old, old, core.PriorityNormal)
	queueCommentsAt(t, s3, "fb", 3, 3, now.Add(-time.Hour), now.Add(-50*time.Minute), core.PriorityNormal)
	got, err = s3.ClaimShared(ctx, core.LaneBatch, core.RouteComment, 4, time.Minute, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ids = map[string]bool{}
	for _, c := range got {
		ids[c.Item.ContentID] = true
	}
	if len(got) != 4 || !ids["fb:comment:3"] || !ids["fb:comment:4"] || !ids["fb:comment:5"] || !ids["fb:comment:0"] {
		t.Fatalf("one platform claimed %v, want its 3 recent ones and its oldest", ids)
	}
}
