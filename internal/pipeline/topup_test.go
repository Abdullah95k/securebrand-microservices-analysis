package pipeline_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/ametrics"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
)

// threadComments are n comments on thread (a post of fb:page:1), enqueued
// interleaved with another thread's: comment i of thread k is the
// (2i+k)-th oldest in the queue.
func threadComments(t *testing.T, e *env, k, n int) {
	t.Helper()
	thread := fmt.Sprintf("fb:post:%d", k+1)
	var items []*core.Item
	for i := range n {
		items = append(items, &core.Item{ContentID: fmt.Sprintf("fb:comment:%d-%d", k, i), Platform: "facebook", Kind: core.KindComment,
			SourceID: "fb:page:1", ThreadID: thread, ParentID: thread, AuthorKey: []byte{byte(k), byte(i)}, CreatedAt: at,
			ObservedAt: at, Text: fmt.Sprintf("تعليق رقم %d على المنشور %d", i, k), Status: core.StatusVisible, Version: 1})
	}
	e.ingest(t, items...)
	for i := range n {
		if _, err := e.s.DB.Exec(context.Background(), `UPDATE analysis.analysis_queue
			SET enqueued_at = now() - interval '1 hour' + make_interval(secs => $2) WHERE content_id = $1`,
			fmt.Sprintf("fb:comment:%d-%d", k, i), 2*i+k); err != nil {
			t.Fatal(err)
		}
	}
}

// requestSizes are the items of each request built, largest first.
func requestSizes(t *testing.T, e *env) []int {
	t.Helper()
	rows, err := e.s.DB.Query(context.Background(), `SELECT items FROM analysis.provider_requests ORDER BY items DESC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

// TestTopUpFillsThreads: a round that claims the 3 oldest comments (two of
// one thread, one of another) also claims those threads' other waiting
// comments, so each thread goes out as one full request instead of the
// claim's scraps.
func TestTopUpFillsThreads(t *testing.T) {
	e := setup(t, func(c *config.Analysis) { c.Batch.MaxItems, c.Batch.TopUpItems = 3, 100 })
	threadComments(t, e, 0, 10)
	threadComments(t, e, 1, 10)
	before := testutil.ToFloat64(ametrics.ToppedUp.WithLabelValues("comment"))
	if err := e.p.SubmitBatches(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := testutil.ToFloat64(ametrics.ToppedUp.WithLabelValues("comment")) - before; d != 17 {
		t.Fatalf("items_topped_up_total rose by %v, want 17 (20 sent, 3 claimed)", d)
	}
	if got := requestSizes(t, e); fmt.Sprint(got) != "[10 10]" {
		t.Fatalf("requests of %v items, want two of 10 (each thread whole)", got)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analysis_queue WHERE request_id IS NULL`); n != 0 {
		t.Fatalf("%d comments left unclaimed", n)
	}
}

// TestTopUpLimits: a thread is topped up to grouping.max_items at most, the
// round by batch.top_up_items at most, and a comment being retried after a
// failure isn't taken (it goes alone, when claimed in turn).
func TestTopUpLimits(t *testing.T) {
	e := setup(t, func(c *config.Analysis) { c.Batch.MaxItems, c.Batch.TopUpItems, c.Grouping.MaxItems = 2, 3, 4 })
	threadComments(t, e, 0, 10) // the 2 oldest are thread 0's: 0-0 and, interleaved, ...
	threadComments(t, e, 1, 10) // ... 1-0
	if _, err := e.s.DB.Exec(context.Background(), `UPDATE analysis.analysis_queue SET attempts = 1 WHERE content_id = 'fb:comment:0-1'`); err != nil {
		t.Fatal(err)
	}
	if err := e.p.SubmitBatches(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Claimed 0-0 and 1-0; topped up by 3 in all: 0-2, 0-3, 0-4 (thread 0
	// first, to its 4, skipping the retried 0-1), none left for thread 1.
	var claimed []string
	rows, err := e.s.DB.Query(context.Background(), `SELECT content_id FROM analysis.analysis_queue WHERE request_id IS NOT NULL ORDER BY content_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		claimed = append(claimed, id)
	}
	rows.Close()
	if fmt.Sprint(claimed) != "[fb:comment:0-0 fb:comment:0-2 fb:comment:0-3 fb:comment:0-4 fb:comment:1-0]" {
		t.Fatalf("sent %v", claimed)
	}
	if got := requestSizes(t, e); fmt.Sprint(got) != "[4 1]" {
		t.Fatalf("requests of %v items, want 4 (thread 0) and 1 (thread 1)", got)
	}
}

// TestTopUpOff: with batch.top_up_items 0 a round sends only what it
// claimed, as before the top-up, and counts nothing as topped up. The
// thread index (migration 00008) is there all the same.
func TestTopUpOff(t *testing.T) {
	e := setup(t, func(c *config.Analysis) { c.Batch.MaxItems, c.Batch.TopUpItems = 3, 0 })
	threadComments(t, e, 0, 10)
	threadComments(t, e, 1, 10)
	before := testutil.ToFloat64(ametrics.ToppedUp.WithLabelValues("comment"))
	if err := e.p.SubmitBatches(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := requestSizes(t, e); fmt.Sprint(got) != "[2 1]" {
		t.Fatalf("requests of %v items, want the claim's 2 and 1", got)
	}
	if n := e.count(t, `SELECT count(*) FROM analysis.analysis_queue WHERE request_id IS NOT NULL`); n != 3 {
		t.Fatalf("%d comments sent, want the 3 claimed", n)
	}
	if d := testutil.ToFloat64(ametrics.ToppedUp.WithLabelValues("comment")) - before; d != 0 {
		t.Fatalf("items_topped_up_total rose by %v with the top-up off", d)
	}
	if n := e.count(t, `SELECT count(*) FROM pg_indexes WHERE schemaname = 'analysis' AND indexname = 'analysis_queue_thread_idx'`); n != 1 {
		t.Fatal("migration 00008's thread index is missing")
	}
}
