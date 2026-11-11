package store

import (
	"context"
	"sort"
	"time"
)

// PlatformStats are one platform's counts since a time: no content.
type PlatformStats struct {
	Platform string
	// Items: first seen since, analyses written since (refused among
	// them), waiting in the queue now, and in the dead letters since.
	Ingested, Analysed, Refused, Waiting, DeadLetters int64
	// OldestWaiting is how long the platform's oldest queue entry has
	// waited (0 with none).
	OldestWaiting time.Duration
	// Media files announced since, by state now; analyses written since.
	// Each done, skipped or failed file has been acknowledged to its
	// collector (gone ones are not: their file was deleted first).
	MediaAnnounced, MediaPending, MediaDone, MediaSkipped, MediaFailed, MediaGone int64
	MediaAnalysed                                                                 int64
	// Why files were skipped (their reason code), with counts.
	MediaReasons map[string]int64
	// Files waiting to be tried again after a failure (now, whenever they
	// were announced), and why files are retrying or failed (failed since):
	// "kind: reason", the error's first 100 characters with digits as #,
	// with counts.
	MediaRetrying    int64
	MediaFailReasons map[string]int64
	// The files pending now, by time to their deadline (the collector
	// deletes the file then): past it, under 12 h, 12-24 h, 24-48 h, later
	// or none; and how many wait for their post (not stored yet).
	PendingPast, Pending12h, Pending24h, Pending48h, PendingLater, PendingForPost int64
}

// PlatformReport is the per-platform counts and the outbox's state; the
// stats command adds the requests and the real-time lane's setup.
type PlatformReport struct {
	Platforms  []PlatformStats
	Outbox     map[string]int64 // records waiting, by topic
	OutboxHeld int64            // of which after a failed publish
	// Provider requests by route and lane, and items sent more than once
	// by route (RequestCounts).
	Requests []RequestStats
	Resent   map[string]Resends
	// The real-time lane's rules: hot accounts in the config, whether the
	// watchlists count, and what is set up (UrgentCounts).
	HotAccounts int
	Watchlists  bool
	Urgent      UrgentSetup
	// Comments received and in the analysed share, by platform
	// (CommentSampleCounts).
	Sample []CommentSample
	// The queue now by route and tries (QueueTriesNow).
	Tries []QueueTries
	// Today's spend by route as the budget counts it (SpendByRoute from
	// the day's start), against the day's cap and the routes' caps, and
	// what the estimate prices requests at where measured (RouteMeasures
	// over the last day); elsewhere DefaultOutPerItem tokens an item and
	// the prefix as a cache miss.
	Spend             []RouteSpend
	DailyCap          float64
	RouteCaps         map[string]float64
	Measures          map[string]RouteMeasure
	DefaultOutPerItem float64
	// The backfills and their progress (Backfills).
	Backfills []Backfill
}

// PlatformCounts counts, per platform, what came in and what came out since t.
// Tables without a platform column (the queue, the dead letters) are
// grouped by the ID's platform code, which name turns into the stored
// platform name ("yt" -> "youtube"). Read-only.
func (s *Store) PlatformCounts(ctx context.Context, since time.Time, name func(code string) string) (PlatformReport, error) {
	by := map[string]*PlatformStats{}
	get := func(p string) *PlatformStats {
		if by[p] == nil {
			by[p] = &PlatformStats{Platform: p, MediaReasons: map[string]int64{}, MediaFailReasons: map[string]int64{}}
		}
		return by[p]
	}
	type q struct {
		sql  string
		scan func(p string, n []int64)
		code bool // the first column is a platform code, not a name
		cols int
	}
	queries := []q{
		{`SELECT platform, count(*) FROM analysis.content_items WHERE first_seen_at >= $1 GROUP BY 1`, func(p string, n []int64) { get(p).Ingested = n[0] }, false, 1},
		{`SELECT platform, count(*), count(*) FILTER (WHERE refused) FROM analysis.analyses WHERE analyzed_at >= $1 GROUP BY 1`,
			func(p string, n []int64) { get(p).Analysed, get(p).Refused = n[0], n[1] }, false, 2},
		{`SELECT split_part(content_id, ':', 1), count(*), greatest(extract(epoch FROM now() - min(enqueued_at)), 0)::bigint
			FROM analysis.analysis_queue WHERE $1::timestamptz IS NOT NULL GROUP BY 1`,
			func(p string, n []int64) {
				get(p).Waiting, get(p).OldestWaiting = n[0], time.Duration(n[1])*time.Second
			}, true, 2},
		{`SELECT split_part(content_id, ':', 1), count(*) FROM analysis.dead_letters WHERE failed_at >= $1 GROUP BY 1`,
			func(p string, n []int64) { get(p).DeadLetters = n[0] }, true, 1},
		{`SELECT platform, count(*),
				count(*) FILTER (WHERE state = 'pending'), count(*) FILTER (WHERE state = 'done'),
				count(*) FILTER (WHERE state = 'skipped'), count(*) FILTER (WHERE state = 'failed'),
				count(*) FILTER (WHERE state = 'gone')
			FROM analysis.media_jobs WHERE received_at >= $1 GROUP BY 1`,
			func(p string, n []int64) {
				m := get(p)
				m.MediaAnnounced, m.MediaPending, m.MediaDone, m.MediaSkipped, m.MediaFailed, m.MediaGone = n[0], n[1], n[2], n[3], n[4], n[5]
			}, false, 6},
		{`SELECT platform, count(*) FROM analysis.media_analyses WHERE analyzed_at >= $1 GROUP BY 1`,
			func(p string, n []int64) { get(p).MediaAnalysed = n[0] }, false, 1},
	}
	for _, qq := range queries {
		rows, err := s.DB.Query(ctx, qq.sql, since)
		if err != nil {
			return PlatformReport{}, err
		}
		for rows.Next() {
			var p string
			n := make([]int64, qq.cols)
			dst := []any{&p}
			for i := range n {
				dst = append(dst, &n[i])
			}
			if err := rows.Scan(dst...); err != nil {
				rows.Close()
				return PlatformReport{}, err
			}
			if qq.code && name != nil {
				p = name(p)
			}
			qq.scan(p, n)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return PlatformReport{}, err
		}
	}
	// Why files were skipped: the reason code it stores ("policy:image/
	// thumbnail", "erased_subject"). A failure keeps an error message,
	// not a code, so failures are only counted.
	rows, err := s.DB.Query(ctx, `SELECT platform, left(coalesce(last_error, ''), 60), count(*)
		FROM analysis.media_jobs WHERE received_at >= $1 AND state = 'skipped' GROUP BY 1, 2`, since)
	if err != nil {
		return PlatformReport{}, err
	}
	for rows.Next() {
		var p, reason string
		var n int64
		if err := rows.Scan(&p, &reason, &n); err != nil {
			rows.Close()
			return PlatformReport{}, err
		}
		if reason == "" {
			reason = "(none)"
		}
		get(p).MediaReasons[reason] += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return PlatformReport{}, err
	}

	// Why files retry or failed: the error's start, digits (addresses,
	// sizes, codes) as #, so like failures count together. An error message
	// names a file at most, never its content.
	rows, err = s.DB.Query(ctx, `SELECT platform, kind, state = 'pending',
			regexp_replace(left(coalesce(last_error, ''), 120), '[0-9]+', '#', 'g'), count(*)
		FROM analysis.media_jobs
		WHERE (state = 'pending' AND tries > 0 AND last_error IS NOT NULL) OR (state = 'failed' AND done_at >= $1)
		GROUP BY 1, 2, 3, 4`, since)
	if err != nil {
		return PlatformReport{}, err
	}
	for rows.Next() {
		var p, kind, reason string
		var retrying bool
		var n int64
		if err := rows.Scan(&p, &kind, &retrying, &reason, &n); err != nil {
			rows.Close()
			return PlatformReport{}, err
		}
		if r := []rune(reason); len(r) > 100 {
			reason = string(r[:100])
		}
		if reason == "" {
			reason = "(none)"
		}
		m := get(p)
		if retrying {
			m.MediaRetrying += n
		}
		m.MediaFailReasons[kind+": "+reason] += n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return PlatformReport{}, err
	}

	// The files pending now, by time to their deadline, and those whose
	// post isn't stored (they wait for it: analysis.media.owner_wait).
	rows, err = s.DB.Query(ctx, `SELECT m.platform,
			count(*) FILTER (WHERE m.delete_after < now()),
			count(*) FILTER (WHERE m.delete_after >= now() AND m.delete_after < now() + interval '12 hours'),
			count(*) FILTER (WHERE m.delete_after >= now() + interval '12 hours' AND m.delete_after < now() + interval '24 hours'),
			count(*) FILTER (WHERE m.delete_after >= now() + interval '24 hours' AND m.delete_after < now() + interval '48 hours'),
			count(*) FILTER (WHERE m.delete_after IS NULL OR m.delete_after >= now() + interval '48 hours'),
			count(*) FILTER (WHERE m.owner_content_id IS NOT NULL AND i.content_id IS NULL)
		FROM analysis.media_jobs m LEFT JOIN analysis.content_index i ON i.content_id = m.owner_content_id
		WHERE m.state = 'pending' AND $1::timestamptz IS NOT NULL GROUP BY 1`, since)
	if err != nil {
		return PlatformReport{}, err
	}
	for rows.Next() {
		var p string
		var n [6]int64
		if err := rows.Scan(&p, &n[0], &n[1], &n[2], &n[3], &n[4], &n[5]); err != nil {
			rows.Close()
			return PlatformReport{}, err
		}
		m := get(p)
		m.PendingPast, m.Pending12h, m.Pending24h, m.Pending48h, m.PendingLater, m.PendingForPost = n[0], n[1], n[2], n[3], n[4], n[5]
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return PlatformReport{}, err
	}

	out := PlatformReport{Outbox: map[string]int64{}}
	rows, err = s.DB.Query(ctx, `SELECT topic, count(*), count(*) FILTER (WHERE attempts > 0) FROM analysis.outbox GROUP BY 1`)
	if err != nil {
		return PlatformReport{}, err
	}
	for rows.Next() {
		var topic string
		var n, held int64
		if err := rows.Scan(&topic, &n, &held); err != nil {
			rows.Close()
			return PlatformReport{}, err
		}
		out.Outbox[topic] = n
		out.OutboxHeld += held
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return PlatformReport{}, err
	}
	for _, p := range by {
		out.Platforms = append(out.Platforms, *p)
	}
	sort.Slice(out.Platforms, func(i, j int) bool { return out.Platforms[i].Platform < out.Platforms[j].Platform })
	return out, nil
}
