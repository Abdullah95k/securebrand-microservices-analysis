package store

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestSpendCountsInFlightAtEstimate: requests in flight count at their
// expected cost, with no discount; their worst case (est_max_usd, or the
// expected cost for requests recorded before it) gives the bound; failed
// requests count nothing.
func TestSpendCountsInFlightAtEstimate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, q := range []string{
		// comment: 200 answered at $0.0001, 100 in flight expected at
		// $0.0001, $0.0008 at worst.
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status, est_cost_usd, est_max_usd, cost_usd)
			SELECT gen_random_uuid(), 'comment', 'batch', 'p', 'm', 1, 'done', 0.0004, 0.0008, 0.0001 FROM generate_series(1, 200)`,
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status, est_cost_usd, est_max_usd)
			SELECT gen_random_uuid(), 'comment', 'batch', 'p', 'm', 1, 'submitted', 0.0001, 0.0008 FROM generate_series(1, 100)`,
		// post: 10 in flight from before est_max_usd: their worst case is
		// their estimate.
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status, est_cost_usd)
			SELECT gen_random_uuid(), 'post', 'batch', 'p', 'm', 1, 'submitted', 0.001 FROM generate_series(1, 10)`,
		// A failed request counts nothing.
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status, est_cost_usd, est_max_usd)
			VALUES (gen_random_uuid(), 'comment', 'batch', 'p', 'm', 1, 'failed', 5, 9)`,
	} {
		if _, err := s.DB.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	since := time.Now().Add(-time.Hour)
	rs, err := s.SpendByRoute(ctx, since)
	if err != nil {
		t.Fatal(err)
	}
	type want struct {
		requests       int64
		counted, worst float64
	}
	wants := map[string]want{"comment": {100, 0.02 + 0.01, 0.02 + 0.08}, "post": {10, 0.01, 0.01}}
	if len(rs) != len(wants) {
		t.Fatalf("routes %+v", rs)
	}
	var total float64
	for _, r := range rs {
		w := wants[r.Route]
		if r.Requests != w.requests || math.Abs(r.Counted-w.counted) > 1e-9 || math.Abs(r.Worst-w.worst) > 1e-9 ||
			math.Abs(r.Counted-(r.Done+r.InFlight)) > 1e-12 || math.Abs(r.Worst-(r.Done+r.InFlightMax)) > 1e-12 {
			t.Errorf("%s: %+v, want %d in flight, counted %v, worst %v", r.Route, r, w.requests, w.counted, w.worst)
		}
		total += r.Counted
	}
	if v, err := s.SpentSince(ctx, since, "comment"); err != nil || math.Abs(v-0.03) > 1e-9 {
		t.Fatalf("SpentSince(comment) %v %v", v, err)
	}
	if v, err := s.SpentSince(ctx, since, ""); err != nil || math.Abs(v-total) > 1e-9 {
		t.Fatalf("SpentSince(all) %v %v, want %v", v, err, total)
	}
}

// TestStartRequestKeepsWorstCase: a request's worst case is stored with
// it, never below its expected cost (a caller that gives none, recording a
// request already answered, stores the expected cost).
func TestStartRequestKeepsWorstCase(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, r := range []RequestRow{
		{ID: uuid.New(), Route: "comment", Lane: "batch", Provider: "p", Model: "m", ContentIDs: []string{"a"}, InputHashes: [][]byte{{1}}, EstCostUSD: 0.0001, EstMaxUSD: 0.0008},
		{ID: uuid.New(), Route: "curation", Lane: "realtime", Provider: "p", Model: "m", ContentIDs: []string{"b"}, InputHashes: [][]byte{{2}}, EstCostUSD: 0.002},
	} {
		if err := s.StartRequest(ctx, r); err != nil {
			t.Fatal(err)
		}
		var est, max float64
		if err := s.DB.QueryRow(ctx, `SELECT est_cost_usd::float8, est_max_usd::float8 FROM analysis.provider_requests WHERE request_id = $1`, r.ID).Scan(&est, &max); err != nil {
			t.Fatal(err)
		}
		if est != r.EstCostUSD || max != math.Max(r.EstMaxUSD, r.EstCostUSD) {
			t.Errorf("%s: stored %v / %v, from %v / %v", r.Route, est, max, r.EstCostUSD, r.EstMaxUSD)
		}
	}
}

// TestRouteMeasures: output tokens per item and the share of the prefix
// read from the cache, over the requests answered since a time, by route,
// for routes with at least MinMeasuredRequests of them; failed and
// in-flight requests don't count.
func TestRouteMeasures(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, q := range []string{
		// comment: 150 one-item requests at 148 and 50 three-item ones at
		// 330: (150×148 + 50×330) / (150 + 150) = 129. Their prefix: 190
		// read 13,000 tokens from the cache, 10 wrote them: 95% read.
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status, output_tokens, cache_read_tokens, cache_write_tokens)
			SELECT gen_random_uuid(), 'comment', 'batch', 'p', 'm', 1, 'done', 148, CASE WHEN g <= 10 THEN 0 ELSE 13000 END,
				CASE WHEN g <= 10 THEN 13000 ELSE 0 END FROM generate_series(1, 150) g`,
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status, output_tokens, cache_read_tokens, cache_write_tokens)
			SELECT gen_random_uuid(), 'comment', 'batch', 'p', 'm', 3, 'done', 330, 13000, 0 FROM generate_series(1, 50)`,
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status, output_tokens, cache_write_tokens)
			SELECT gen_random_uuid(), 'comment', 'batch', 'p', 'm', 1, 'failed', 9999, 13000 FROM generate_series(1, 50)`,
		// post: 99 answered, one short of a measure; 500 in flight.
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status, output_tokens)
			SELECT gen_random_uuid(), 'post', 'batch', 'p', 'm', 1, 'done', 400 FROM generate_series(1, 99)`,
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status)
			SELECT gen_random_uuid(), 'post', 'batch', 'p', 'm', 1, 'submitted' FROM generate_series(1, 500)`,
		// video: 100 answered with no cache recorded: no hits.
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status, output_tokens)
			SELECT gen_random_uuid(), 'video', 'realtime', 'p', 'm', 1, 'done', 250 FROM generate_series(1, 100)`,
		// image: answered before the window.
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status, output_tokens, created_at)
			SELECT gen_random_uuid(), 'image', 'realtime', 'p', 'm', 1, 'done', 250, now() - interval '2 days' FROM generate_series(1, 200)`,
	} {
		if _, err := s.DB.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.RouteMeasures(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	c, v := got["comment"], got["video"]
	if len(got) != 2 || c.Requests != 200 || math.Abs(c.OutPerItem-129) > 1e-9 || math.Abs(c.CacheHit-0.95) > 1e-9 ||
		v.Requests != 100 || v.OutPerItem != 250 || v.CacheHit != 0 {
		t.Fatalf("measures %+v, want comment (200, 129, 0.95) and video (100, 250, 0)", got)
	}
}
