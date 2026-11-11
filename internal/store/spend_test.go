package store

import (
	"context"
	"math"
	"testing"
	"time"
)

// TestSpendCountsInFlightAtMeasuredRatio: requests in flight count at
// their estimate times the route's measured actual ÷ estimate (last day,
// at least 100 answered), never below MinSpendRatio nor above 1; failed
// requests count nothing.
func TestSpendCountsInFlightAtMeasuredRatio(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, q := range []string{
		// comment: 200 answered at a quarter of their estimate (ratio 0.25),
		// 100 in flight at $0.0004: 200 × 0.0001 + 100 × 0.0004 × 0.25.
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status, est_cost_usd, cost_usd)
			SELECT gen_random_uuid(), 'comment', 'batch', 'p', 'm', 1, 'done', 0.0004, 0.0001 FROM generate_series(1, 200)`,
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status, est_cost_usd)
			SELECT gen_random_uuid(), 'comment', 'batch', 'p', 'm', 1, 'submitted', 0.0004 FROM generate_series(1, 100)`,
		// post: only 50 answered, too few to measure: in flight at the full estimate.
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status, est_cost_usd, cost_usd)
			SELECT gen_random_uuid(), 'post', 'batch', 'p', 'm', 1, 'done', 0.001, 0.0002 FROM generate_series(1, 50)`,
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status, est_cost_usd)
			SELECT gen_random_uuid(), 'post', 'batch', 'p', 'm', 1, 'submitted', 0.001 FROM generate_series(1, 10)`,
		// image: answered at 5 times the estimate: the ratio is capped at 1.
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status, est_cost_usd, cost_usd)
			SELECT gen_random_uuid(), 'image', 'realtime', 'p', 'm', 1, 'done', 0.0001, 0.0005 FROM generate_series(1, 150)`,
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status, est_cost_usd)
			SELECT gen_random_uuid(), 'image', 'realtime', 'p', 'm', 1, 'submitted', 0.0001 FROM generate_series(1, 10)`,
		// A failed request counts nothing.
		`INSERT INTO analysis.provider_requests (request_id, route, lane, provider, model, items, status, est_cost_usd)
			VALUES (gen_random_uuid(), 'comment', 'batch', 'p', 'm', 1, 'failed', 5)`,
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
	want := map[string][2]float64{"comment": {0.25, 0.02 + 0.01}, "post": {1, 0.01 + 0.01}, "image": {1, 0.075 + 0.001}}
	if len(rs) != len(want) {
		t.Fatalf("routes %+v", rs)
	}
	var total float64
	for _, r := range rs {
		w := want[r.Route]
		if math.Abs(r.Ratio-w[0]) > 1e-9 || math.Abs(r.Counted-w[1]) > 1e-9 {
			t.Errorf("%s: ratio %v counted %v, want %v and %v", r.Route, r.Ratio, r.Counted, w[0], w[1])
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
