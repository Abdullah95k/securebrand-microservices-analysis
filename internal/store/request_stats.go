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
	// Why requests failed: the error's first 100 characters with digits as
	// #, with counts. A failed request got no answer (never sent, aborted,
	// errored, expired), so nothing was paid for it.
	FailReasons map[string]int64
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
	at := map[[2]string]int{}
	for i, r := range out {
		at[[2]string{r.Route, r.Lane}] = i
	}
	rows, err = s.DB.Query(ctx, `SELECT route, lane, left(regexp_replace(coalesce(error, 'no reason'), '[0-9]+', '#', 'g'), 100), count(*)
		FROM analysis.provider_requests WHERE created_at >= $1 AND status = 'failed' GROUP BY 1, 2, 3`, since)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var route, lane, reason string
		var n int64
		if err := rows.Scan(&route, &lane, &reason, &n); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if i, ok := at[[2]string{route, lane}]; ok {
			if out[i].FailReasons == nil {
				out[i].FailReasons = map[string]int64{}
			}
			out[i].FailReasons[reason] += n
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
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

// CommentSample is one platform's comment records since a day (Iraq
// days): received, in the sample, kept outside it as real-time items, and
// the share in force on the last of those days.
type CommentSample struct {
	Platform                  string
	Received, Sampled, Urgent int64
	Share                     float64
}

// CommentSampleCounts sums analysis.comment_sample_daily from the Iraq day
// holding since. Read-only.
func (s *Store) CommentSampleCounts(ctx context.Context, since time.Time) ([]CommentSample, error) {
	rows, err := s.DB.Query(ctx, `SELECT platform, sum(received)::bigint, sum(sampled)::bigint, sum(urgent)::bigint,
			(array_agg(share ORDER BY day DESC))[1]::float8
		FROM analysis.comment_sample_daily WHERE day >= $1::date GROUP BY 1 ORDER BY 1`, since.In(s.Loc).Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CommentSample
	for rows.Next() {
		var c CommentSample
		if err := rows.Scan(&c.Platform, &c.Received, &c.Sampled, &c.Urgent, &c.Share); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// QueueTries is one route's queue now by tries counted (a claim counts one;
// an entry in flight counts its current try), and why entries that came
// back did: their last error's first 100 characters with digits as #, and
// whether it was about their own answer (ItemFault, so they go alone).
type QueueTries struct {
	Route                       string
	None, One, Two, ThreeOrMore int64
	Reasons                     map[string]int64
	ItemFaults                  int64
}

// QueueTriesNow counts the queue by route and tries, and the last errors
// of entries that came back. Read-only; it scans the queue.
func (s *Store) QueueTriesNow(ctx context.Context) ([]QueueTries, error) {
	rows, err := s.DB.Query(ctx, `SELECT route, count(*) FILTER (WHERE attempts = 0), count(*) FILTER (WHERE attempts = 1),
			count(*) FILTER (WHERE attempts = 2), count(*) FILTER (WHERE attempts >= 3),
			count(*) FILTER (WHERE `+itemFaultSQL+`)
		FROM analysis.analysis_queue GROUP BY 1 ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	var out []QueueTries
	at := map[string]int{}
	for rows.Next() {
		var q QueueTries
		if err := rows.Scan(&q.Route, &q.None, &q.One, &q.Two, &q.ThreeOrMore, &q.ItemFaults); err != nil {
			rows.Close()
			return nil, err
		}
		q.Reasons = map[string]int64{}
		at[q.Route] = len(out)
		out = append(out, q)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = s.DB.Query(ctx, `SELECT route, left(regexp_replace(coalesce(last_error, 'none recorded'), '[0-9]+', '#', 'g'), 100), count(*)
		FROM analysis.analysis_queue WHERE attempts >= 1 GROUP BY 1, 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var route, reason string
		var n int64
		if err := rows.Scan(&route, &reason, &n); err != nil {
			return nil, err
		}
		if i, ok := at[route]; ok {
			out[i].Reasons[reason] += n
		}
	}
	return out, rows.Err()
}
