package store

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/textnorm"
)

// Claimed is a queue entry leased to a worker, with its item.
type Claimed struct {
	Item     core.Item
	Route    string
	Lane     string
	Priority int
	Reason   string
	Attempts int
}

// Claim leases up to limit queue entries of a lane and route, oldest first
// within priority, enqueued at least minAge ago (so a thread's siblings can
// arrive), and loads their items, returned in that order.
func (s *Store) Claim(ctx context.Context, lane, route string, limit int, lease, minAge time.Duration) ([]Claimed, error) {
	rows, err := s.DB.Query(ctx, `
		WITH c AS (
			SELECT content_id, route FROM analysis.analysis_queue
			WHERE lane = $1 AND route = $2 AND not_before <= now()
			  AND (lease_until IS NULL OR lease_until < now())
			  AND enqueued_at <= now() - make_interval(secs => $5)
			ORDER BY priority, enqueued_at
			LIMIT $3 FOR UPDATE SKIP LOCKED),
		u AS (
			UPDATE analysis.analysis_queue q SET lease_until = now() + make_interval(secs => $4), attempts = q.attempts + 1
			FROM c WHERE q.content_id = c.content_id AND q.route = c.route
			RETURNING q.content_id, q.created_at, q.route, q.lane, q.priority, q.reason, q.attempts, q.enqueued_at)
		-- Oldest first, as claimed: groups and their requests follow this order.
		SELECT content_id, created_at, route, lane, priority, reason, attempts FROM u ORDER BY priority, enqueued_at, content_id`,
		lane, route, limit, lease.Seconds(), minAge.Seconds())
	if err != nil {
		return nil, err
	}
	var out []Claimed
	for rows.Next() {
		var c Claimed
		if err := rows.Scan(&c.Item.ContentID, &c.Item.CreatedAt, &c.Route, &c.Lane, &c.Priority, &c.Reason, &c.Attempts); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return nil, err
	}
	return s.loadClaimed(ctx, out)
}

// ClaimThreads tops up a claim with more waiting entries of the given
// threads (same lane and route): up to room[i] for threads[i], oldest first
// within priority, and at most limit in all, threads in the order given. Only entries never claimed
// before (attempts 0) are taken: one retried after a failure is sent alone
// anyway. A round's requests then carry a thread's waiting comments
// together, so the cached prompt is read once for many of them.
func (s *Store) ClaimThreads(ctx context.Context, lane, route string, threads []string, room []int, limit int, lease time.Duration) ([]Claimed, error) {
	if len(threads) == 0 || len(threads) != len(room) || limit <= 0 {
		return nil, nil
	}
	rows, err := s.DB.Query(ctx, `
		WITH c AS (
			SELECT x.content_id, x.route FROM unnest($3::text[], $4::int[]) AS t(thread_id, room)
			CROSS JOIN LATERAL (
				SELECT content_id, route FROM analysis.analysis_queue
				WHERE thread_id = t.thread_id AND route = $2 AND lane = $1 AND attempts = 0
				  AND not_before <= now() AND (lease_until IS NULL OR lease_until < now())
				ORDER BY priority, enqueued_at
				LIMIT t.room FOR UPDATE SKIP LOCKED) x
			LIMIT $5),
		u AS (
			UPDATE analysis.analysis_queue q SET lease_until = now() + make_interval(secs => $6), attempts = q.attempts + 1
			FROM c WHERE q.content_id = c.content_id AND q.route = c.route
			RETURNING q.content_id, q.created_at, q.route, q.lane, q.priority, q.reason, q.attempts, q.enqueued_at)
		SELECT content_id, created_at, route, lane, priority, reason, attempts FROM u ORDER BY priority, enqueued_at, content_id`,
		lane, route, threads, room, limit, lease.Seconds())
	if err != nil {
		return nil, err
	}
	var out []Claimed
	for rows.Next() {
		var c Claimed
		if err := rows.Scan(&c.Item.ContentID, &c.Item.CreatedAt, &c.Route, &c.Lane, &c.Priority, &c.Reason, &c.Attempts); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return nil, err
	}
	return s.loadClaimed(ctx, out)
}

// loadClaimed fills the claimed entries' items; entries whose item vanished
// (erased) are dropped from the queue.
func (s *Store) loadClaimed(ctx context.Context, cs []Claimed) ([]Claimed, error) {
	ats := make([]time.Time, len(cs))
	ids := make([]string, len(cs))
	for i, c := range cs {
		ats[i], ids[i] = c.Item.CreatedAt, c.Item.ContentID
	}
	items, err := s.LoadItems(ctx, ats, ids)
	if err != nil {
		return nil, err
	}
	out := cs[:0]
	var gone []string
	for _, c := range cs {
		it, ok := items[c.Item.ContentID]
		if !ok || it.Text == "" || it.Status == core.StatusRedacted {
			gone = append(gone, c.Item.ContentID)
			continue
		}
		c.Item = it
		out = append(out, c)
	}
	if len(gone) > 0 {
		if _, err := s.DB.Exec(ctx, `DELETE FROM analysis.analysis_queue WHERE content_id = ANY($1)`, gone); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// LoadItems loads items by primary key.
func (s *Store) LoadItems(ctx context.Context, createdAt []time.Time, ids []string) (map[string]core.Item, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT c.content_id, c.created_at, c.platform, c.kind, c.source_id, coalesce(c.source_name, ''), coalesce(c.thread_id, ''),
		       coalesce(c.parent_id, ''), c.author_key, c.author_is_source, coalesce(c.text, ''), coalesce(c.lang_hint, ''),
		       coalesce(c.hashtags, '{}'), coalesce(c.media_ids, '{}'), c.status, coalesce(c.tag_spans, '{}'),
		       c.content_version, c.depth, coalesce(c.author_id, '')
		FROM unnest($1::timestamptz[], $2::text[]) AS k(created_at, content_id)
		JOIN analysis.content_items c ON c.created_at = k.created_at AND c.content_id = k.content_id`, createdAt, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]core.Item{}
	for rows.Next() {
		var it core.Item
		var spans []int32
		if err := rows.Scan(&it.ContentID, &it.CreatedAt, &it.Platform, &it.Kind, &it.SourceID, &it.SourceName, &it.ThreadID,
			&it.ParentID, &it.AuthorKey, &it.AuthorIsSource, &it.Text, &it.LangHint, &it.Hashtags, &it.MediaIDs, &it.Status, &spans,
			&it.Version, &it.Depth, &it.AuthorID); err != nil {
			return nil, err
		}
		it.RemoveSpans = textnorm.IntsToSpans(spans)
		out[it.ContentID] = it
	}
	return out, rows.Err()
}

// ItemsByID loads items by content id through the index (thread posts and
// parent comments).
func (s *Store) ItemsByID(ctx context.Context, ids []string) (map[string]core.Item, error) {
	if len(ids) == 0 {
		return map[string]core.Item{}, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT content_id, created_at FROM analysis.content_index WHERE content_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	var ats []time.Time
	var keys []string
	for rows.Next() {
		var id string
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			rows.Close()
			return nil, err
		}
		ats, keys = append(ats, at), append(keys, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s.LoadItems(ctx, ats, keys)
}

// RequestRow is a provider request recorded before it is sent.
type RequestRow struct {
	ID         uuid.UUID
	BatchID    string
	Route      string
	Lane       string
	Provider   string
	Model      string
	Fallback   bool
	ContentIDs []string
	// InputHashes are the items' input hashes when sent, in ContentIDs order.
	InputHashes [][]byte
	EstCostUSD  float64
}

// StartRequest records a provider request and ties its queue entries to it.
func (s *Store) StartRequest(ctx context.Context, r RequestRow) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO analysis.provider_requests (request_id, batch_id, route, lane, provider, model,
			items, content_ids, input_hashes, status, fallback, est_cost_usd)
			VALUES ($1, NULLIF($2,''), $3, $4, $5, $6, $7, $8, $9, 'submitted', $10, $11)`,
			r.ID, r.BatchID, r.Route, r.Lane, r.Provider, r.Model, len(r.ContentIDs), r.ContentIDs, r.InputHashes, r.Fallback, r.EstCostUSD); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE analysis.analysis_queue SET request_id = $1 WHERE content_id = ANY($2) AND route = $3`,
			r.ID, r.ContentIDs, r.Route)
		return err
	})
}

// SetBatchID records the provider batch of submitted requests, and leases
// their queue entries for lease (the batch's lifetime): until then they
// were leased only for the submission (batch.submit_lease).
func (s *Store) SetBatchID(ctx context.Context, batchID, providerName, model, route string, ids []uuid.UUID, lease time.Duration) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO analysis.provider_batches (batch_id, provider, model, route, status, requests)
			VALUES ($1, $2, $3, $4, 'in_progress', $5) ON CONFLICT (batch_id) DO NOTHING`,
			batchID, providerName, model, route, len(ids)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE analysis.provider_requests SET batch_id = $1 WHERE request_id = ANY($2)`, batchID, ids); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE analysis.analysis_queue SET lease_until = now() + make_interval(secs => $2)
			WHERE request_id = ANY($1)`, ids, lease.Seconds())
		return err
	})
}

// ReleaseStrandedBatch gives back batch-lane work that a process claimed
// but never sent: it stopped (a crash, a restart) or failed between the
// claim and the provider's answer. It finds
//   - requests recorded for a batch but never given one (no batch_id),
//     older than olderThan: marked failed, so their estimated cost no longer
//     counts against the day's budget, and their items released;
//   - items leased for longer than olderThan from now whose request isn't
//     in a sent batch (a long lease is only given with the batch): released.
//
// Released items lose the attempt their claim counted. It returns the
// requests closed and the items released.
func (s *Store) ReleaseStrandedBatch(ctx context.Context, olderThan time.Duration) (requests, items int64, err error) {
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var released int64
		if err := tx.QueryRow(ctx, `WITH r AS (
				UPDATE analysis.provider_requests SET status = 'failed', completed_at = now(),
					error = 'never sent: the process stopped or failed before its batch was submitted'
				WHERE lane = 'batch' AND status = 'submitted' AND batch_id IS NULL
				  AND created_at < now() - make_interval(secs => $1)
				RETURNING request_id),
			q AS (
				UPDATE analysis.analysis_queue q SET lease_until = NULL, request_id = NULL, attempts = greatest(q.attempts - 1, 0)
				FROM r WHERE q.request_id = r.request_id
				RETURNING 1)
			SELECT (SELECT count(*) FROM r), (SELECT count(*) FROM q)`, olderThan.Seconds()).Scan(&requests, &released); err != nil {
			return err
		}
		items += released
		tag, err := tx.Exec(ctx, `UPDATE analysis.analysis_queue q SET lease_until = NULL, request_id = NULL,
				attempts = greatest(q.attempts - 1, 0)
			WHERE q.lane = 'batch' AND q.lease_until > now() + make_interval(secs => $1)
			  AND NOT EXISTS (SELECT 1 FROM analysis.provider_requests r
			                  WHERE r.request_id = q.request_id AND r.batch_id IS NOT NULL AND r.status = 'submitted')`,
			olderThan.Seconds())
		if err != nil {
			return err
		}
		items += tag.RowsAffected()
		return nil
	})
	return requests, items, err
}

// FailRequest marks a request failed and releases its queue entries with a
// backoff; entries out of attempts go to the dead letters.
func (s *Store) FailRequest(ctx context.Context, id uuid.UUID, reason string, maxAttempts int) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE analysis.provider_requests SET status = 'failed', error = left($2, 500), completed_at = now()
			WHERE request_id = $1`, id, reason); err != nil {
			return err
		}
		return releaseTx(ctx, tx, id, nil, reason, maxAttempts)
	})
}

// releaseTx returns queue entries of a request (all, or only ids) to the
// queue with exponential backoff, or to the dead letters when out of
// attempts.
func releaseTx(ctx context.Context, tx pgx.Tx, req uuid.UUID, ids []string, reason string, maxAttempts int) error {
	filter := `request_id = $1`
	args := []any{req, reason, maxAttempts}
	if ids != nil {
		filter += ` AND content_id = ANY($4)`
		args = append(args, ids)
	}
	if _, err := tx.Exec(ctx, `
		WITH dead AS (
			DELETE FROM analysis.analysis_queue WHERE `+filter+` AND attempts >= $3
			RETURNING content_id, route, attempts)
		INSERT INTO analysis.dead_letters (content_id, route, attempts, error)
		SELECT content_id, route, attempts, left($2, 500) FROM dead
		ON CONFLICT (content_id, route) DO UPDATE SET attempts = EXCLUDED.attempts, error = EXCLUDED.error, failed_at = now()`,
		args...); err != nil {
		return fmt.Errorf("dead letters: %w", err)
	}
	_, err := tx.Exec(ctx, `UPDATE analysis.analysis_queue SET lease_until = NULL, request_id = NULL, last_error = left($2, 500),
		not_before = now() + make_interval(secs => least(3600, 30 * power(2, attempts)))
		WHERE `+filter+` AND ($3 > 0)`, args...)
	return err
}

// ReleaseUnsent returns claimed entries that were never sent (budget pause,
// provider unavailable) without counting an attempt.
func (s *Store) ReleaseUnsent(ctx context.Context, ids []string, route string, delay time.Duration) error {
	_, err := s.DB.Exec(ctx, `UPDATE analysis.analysis_queue SET lease_until = NULL, request_id = NULL,
		attempts = greatest(attempts - 1, 0), not_before = now() + make_interval(secs => $3)
		WHERE content_id = ANY($1) AND route = $2`, ids, route, delay.Seconds())
	return err
}

// Enqueue adds queue entries (re-analysis, escalation, audit).
func (s *Store) Enqueue(ctx context.Context, items []core.Item, route, lane string, priority int, reason string) (int64, error) {
	if len(items) == 0 {
		return 0, nil
	}
	b := &pgx.Batch{}
	for _, it := range items {
		b.Queue(`INSERT INTO analysis.analysis_queue (content_id, route, created_at, thread_id, lane, priority, reason)
			VALUES ($1,$2,$3,NULLIF($4,''),$5,$6,$7) ON CONFLICT (content_id, route) DO NOTHING`,
			it.ContentID, route, it.CreatedAt, it.ThreadID, lane, priority, reason)
	}
	br := s.DB.SendBatch(ctx, b)
	defer func() { _ = br.Close() }()
	var n int64
	for range items {
		tag, err := br.Exec()
		if err != nil {
			return n, err
		}
		n += tag.RowsAffected()
	}
	return n, nil
}

// QueueStats is the depth and oldest age of a lane and route.
type QueueStats struct {
	Lane, Route string
	Depth       int64
	OldestAge   time.Duration
}

// Stats reports the queue per lane and route.
func (s *Store) Stats(ctx context.Context) ([]QueueStats, error) {
	rows, err := s.DB.Query(ctx, `SELECT lane, route, count(*), coalesce(extract(epoch FROM now() - min(enqueued_at)), 0)
		FROM analysis.analysis_queue GROUP BY lane, route`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QueueStats
	for rows.Next() {
		var q QueueStats
		var age float64
		if err := rows.Scan(&q.Lane, &q.Route, &q.Depth, &age); err != nil {
			return nil, err
		}
		q.OldestAge = time.Duration(math.Max(age, 0) * float64(time.Second))
		out = append(out, q)
	}
	return out, rows.Err()
}

// SpentSince is the spend recorded since t: actual cost where known, the
// estimate for requests still in flight.
func (s *Store) SpentSince(ctx context.Context, t time.Time, route string) (float64, error) {
	var v float64
	err := s.DB.QueryRow(ctx, `SELECT coalesce(sum(coalesce(cost_usd, est_cost_usd, 0)), 0)::float8
		FROM analysis.provider_requests WHERE created_at >= $1 AND ($2 = '' OR route = $2) AND status <> 'failed'`, t, route).Scan(&v)
	return v, err
}

// OpenBatch is a provider batch to poll.
type OpenBatch struct {
	ID, Provider, Model, Route string
	SubmittedAt                time.Time
}

// ClaimOpenBatches leases batches still in progress for polling.
func (s *Store) ClaimOpenBatches(ctx context.Context, limit int, lease time.Duration) ([]OpenBatch, error) {
	rows, err := s.DB.Query(ctx, `
		WITH c AS (SELECT batch_id FROM analysis.provider_batches WHERE status = 'in_progress'
		           AND (lease_until IS NULL OR lease_until < now()) ORDER BY submitted_at LIMIT $1 FOR UPDATE SKIP LOCKED)
		UPDATE analysis.provider_batches b SET lease_until = now() + make_interval(secs => $2)
		FROM c WHERE b.batch_id = c.batch_id
		RETURNING b.batch_id, b.provider, b.model, b.route, b.submitted_at`, limit, lease.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OpenBatch
	for rows.Next() {
		var b OpenBatch
		if err := rows.Scan(&b.ID, &b.Provider, &b.Model, &b.Route, &b.SubmittedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BatchInfo is a provider batch and the states of its requests here.
type BatchInfo struct {
	ID, Provider, Model, Route, Status string
	Requests                           int
	SubmittedAt                        time.Time
	EndedAt                            *time.Time
	// The batch's provider_requests by status.
	Submitted, Done, Failed int
}

// RecentBatches lists the batches still open and those submitted since t,
// oldest first.
func (s *Store) RecentBatches(ctx context.Context, since time.Time) ([]BatchInfo, error) {
	rows, err := s.DB.Query(ctx, `
		SELECT b.batch_id, b.provider, b.model, b.route, b.status, b.requests, b.submitted_at, b.ended_at,
			count(r.request_id) FILTER (WHERE r.status = 'submitted'),
			count(r.request_id) FILTER (WHERE r.status = 'done'),
			count(r.request_id) FILTER (WHERE r.status = 'failed')
		FROM analysis.provider_batches b LEFT JOIN analysis.provider_requests r ON r.batch_id = b.batch_id
		WHERE b.status = 'in_progress' OR b.submitted_at >= $1
		GROUP BY b.batch_id ORDER BY b.submitted_at`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BatchInfo
	for rows.Next() {
		var b BatchInfo
		if err := rows.Scan(&b.ID, &b.Provider, &b.Model, &b.Route, &b.Status, &b.Requests, &b.SubmittedAt, &b.EndedAt,
			&b.Submitted, &b.Done, &b.Failed); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ReleaseBatch drops the poll lease of a batch still running.
func (s *Store) ReleaseBatch(ctx context.Context, id string) error {
	_, err := s.DB.Exec(ctx, `UPDATE analysis.provider_batches SET lease_until = NULL WHERE batch_id = $1`, id)
	return err
}

// EndBatch marks a batch ended (or failed with a reason).
func (s *Store) EndBatch(ctx context.Context, id, status, reason string) error {
	_, err := s.DB.Exec(ctx, `UPDATE analysis.provider_batches SET status = $2, ended_at = now(), lease_until = NULL,
		last_error = NULLIF(left($3, 500), '') WHERE batch_id = $1`, id, status, reason)
	return err
}

// SentRequest is a recorded request with its items in prompt order.
type SentRequest struct {
	ID          uuid.UUID
	Route, Lane string
	Provider    string
	Model       string
	Fallback    bool
	Status      string
	ContentIDs  []string
	InputHashes [][]byte
}

// Request loads a recorded request.
func (s *Store) Request(ctx context.Context, id uuid.UUID) (*SentRequest, error) {
	r := &SentRequest{ID: id}
	err := s.DB.QueryRow(ctx, `SELECT route, lane, provider, model, fallback, status, content_ids, input_hashes
		FROM analysis.provider_requests WHERE request_id = $1`, id).Scan(&r.Route, &r.Lane, &r.Provider, &r.Model, &r.Fallback,
		&r.Status, &r.ContentIDs, &r.InputHashes)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// AbortRequests marks requests failed without counting an attempt (they
// were never processed: submission failed) and returns their items to the
// queue after delay.
func (s *Store) AbortRequests(ctx context.Context, ids []uuid.UUID, reason string, delay time.Duration) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE analysis.provider_requests SET status = 'failed', error = left($2, 500), completed_at = now()
			WHERE request_id = ANY($1) AND status = 'submitted'`, ids, reason); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE analysis.analysis_queue SET lease_until = NULL, request_id = NULL, last_error = left($2, 500),
			attempts = greatest(attempts - 1, 0), not_before = now() + make_interval(secs => $3) WHERE request_id = ANY($1)`,
			ids, reason, delay.Seconds())
		return err
	})
}

// StaleRequests lists requests of an ended batch that got no result.
func (s *Store) StaleRequests(ctx context.Context, batchID string) ([]uuid.UUID, error) {
	rows, err := s.DB.Query(ctx, `SELECT request_id FROM analysis.provider_requests WHERE batch_id = $1 AND status = 'submitted'`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// DeadLetterCount is the number of items given up after max attempts.
func (s *Store) DeadLetterCount(ctx context.Context) (int64, error) {
	var n int64
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM analysis.dead_letters`).Scan(&n)
	return n, err
}

// QueueDepth counts the items waiting for analysis (all lanes and routes).
func (s *Store) QueueDepth(ctx context.Context) (int64, error) {
	var n int64
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM analysis.analysis_queue`).Scan(&n)
	return n, err
}
