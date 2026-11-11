package store

import (
	"context"
	"sort"
	"time"
)

// RequestStats are one route and lane's provider requests since a time:
// how full they were and what they cost. Counts only, no content.
type RequestStats struct {
	Route, Lane string
	// Requests sent since: done, failed, still in flight; the items they
	// carried, and the requests that carried only one.
	Requests, Done, Failed, InFlight, Items, OneItem int64
	// The done requests' items, tokens and real cost.
	DoneItems                                                    int64
	InputTokens, CacheReadTokens, CacheWriteTokens, OutputTokens int64
	CostUSD                                                      float64
}

// Resends are a route's items sent in more than one request since a time
// (a retry after a failure, an errored or expired batch request), and the
// sends beyond each item's first.
type Resends struct {
	Items, ExtraSends int64
}

// RequestCounts counts provider requests since t by route and lane, and the
// items sent more than once by route. Read-only.
func (s *Store) RequestCounts(ctx context.Context, since time.Time) ([]RequestStats, map[string]Resends, error) {
	rows, err := s.DB.Query(ctx, `SELECT route, lane, count(*),
			count(*) FILTER (WHERE status = 'done'), count(*) FILTER (WHERE status = 'failed'),
			count(*) FILTER (WHERE status = 'submitted'), coalesce(sum(items), 0), count(*) FILTER (WHERE items = 1),
			coalesce(sum(items) FILTER (WHERE status = 'done'), 0),
			coalesce(sum(input_tokens) FILTER (WHERE status = 'done'), 0),
			coalesce(sum(cache_read_tokens) FILTER (WHERE status = 'done'), 0),
			coalesce(sum(cache_write_tokens) FILTER (WHERE status = 'done'), 0),
			coalesce(sum(output_tokens) FILTER (WHERE status = 'done'), 0),
			coalesce(sum(cost_usd) FILTER (WHERE status = 'done'), 0)::float8
		FROM analysis.provider_requests WHERE created_at >= $1 GROUP BY 1, 2`, since)
	if err != nil {
		return nil, nil, err
	}
	var out []RequestStats
	for rows.Next() {
		var r RequestStats
		if err := rows.Scan(&r.Route, &r.Lane, &r.Requests, &r.Done, &r.Failed, &r.InFlight, &r.Items, &r.OneItem,
			&r.DoneItems, &r.InputTokens, &r.CacheReadTokens, &r.CacheWriteTokens, &r.OutputTokens, &r.CostUSD); err != nil {
			rows.Close()
			return nil, nil, err
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Route != out[j].Route {
			return out[i].Route < out[j].Route
		}
		return out[i].Lane < out[j].Lane
	})
	resent := map[string]Resends{}
	rows, err = s.DB.Query(ctx, `SELECT route, count(*), coalesce(sum(n - 1), 0) FROM (
			SELECT r.route, cid, count(*) AS n FROM analysis.provider_requests r, unnest(r.content_ids) AS cid
			WHERE r.created_at >= $1 GROUP BY 1, 2 HAVING count(*) > 1) x
		GROUP BY 1`, since)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var route string
		var rs Resends
		if err := rows.Scan(&route, &rs.Items, &rs.ExtraSends); err != nil {
			return nil, nil, err
		}
		resent[route] = rs
	}
	return out, resent, rows.Err()
}

// UrgentSetup is what the real-time lane's rules have to work with: the
// accounts clients monitor (watchlists), the clients, and the entities
// clients track. Counts only.
type UrgentSetup struct {
	MonitoredSources, Clients, TrackedEntities int64
}

// UrgentCounts counts the watchlists and tracked entities. Read-only.
func (s *Store) UrgentCounts(ctx context.Context) (UrgentSetup, error) {
	var u UrgentSetup
	err := s.DB.QueryRow(ctx, `SELECT (SELECT count(DISTINCT source_id) FROM analysis.client_sources),
		(SELECT count(*) FROM analysis.clients), (SELECT count(DISTINCT entity_id) FROM analysis.client_entities)`).
		Scan(&u.MonitoredSources, &u.Clients, &u.TrackedEntities)
	return u, err
}
