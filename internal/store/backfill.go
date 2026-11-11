package store

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
)

// BackfillPages is the backfill that gives short posts queued before
// migration 00011 their page (analysis_queue.source_id), so the batch
// round's page top-up finds them.
const BackfillPages = "queue-source-id"

// lockBackfill keeps two replicas from running a pass at once.
const lockBackfill = 4200107

// Backfill is a backfill's progress.
type Backfill struct {
	Name string
	// Scanned queue entries so far, of which Updated were given a value.
	Scanned, Updated int64
	StartedAt        time.Time
	// LastAt is the last pass (zero before the first); FinishedAt when a
	// pass reached the end (zero while running).
	LastAt, FinishedAt time.Time
}

// BackfillPagesPass reads the next rows comment-route queue entries in
// content-ID order, from where the last pass stopped, and gives the short
// posts among them their page. It reports whether the backfill has
// reached the end: a pass reads fewer than rows. If another replica holds
// the pass, it does nothing.
func (s *Store) BackfillPagesPass(ctx context.Context, rows int) (done bool, err error) {
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, lockBackfill).Scan(&locked); err != nil || !locked {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO analysis.backfills (name) VALUES ($1) ON CONFLICT (name) DO NOTHING`, BackfillPages); err != nil {
			return err
		}
		var cursor string
		var finished *time.Time
		if err := tx.QueryRow(ctx, `SELECT cursor, finished_at FROM analysis.backfills WHERE name = $1`, BackfillPages).
			Scan(&cursor, &finished); err != nil {
			return err
		}
		if finished != nil {
			done = true
			return nil
		}
		var last string
		var scanned, updated int64
		if err := tx.QueryRow(ctx, `
			WITH s AS (
				SELECT content_id, created_at FROM analysis.analysis_queue
				WHERE route = $2 AND content_id > $1 ORDER BY content_id LIMIT $3),
			u AS (
				UPDATE analysis.analysis_queue q SET source_id = c.source_id
				FROM s JOIN analysis.content_items c ON c.created_at = s.created_at AND c.content_id = s.content_id
				WHERE q.content_id = s.content_id AND q.route = $2 AND q.source_id IS NULL
				  AND c.kind = $4 AND c.source_id IS NOT NULL AND c.source_id <> ''
				RETURNING 1)
			SELECT coalesce((SELECT max(content_id) FROM s), $1), (SELECT count(*) FROM s), (SELECT count(*) FROM u)`,
			cursor, core.RouteComment, rows, core.KindPost).Scan(&last, &scanned, &updated); err != nil {
			return err
		}
		done = scanned < int64(rows)
		_, err := tx.Exec(ctx, `UPDATE analysis.backfills SET cursor = $2, scanned = scanned + $3, updated = updated + $4,
			last_at = now(), finished_at = CASE WHEN $5 THEN now() END WHERE name = $1`,
			BackfillPages, last, scanned, updated, done)
		return err
	})
	return done, err
}

// Backfill pass size and pause (the master, 11 Nov: about 10,000 rows a
// pass with a pause between, so the live queue is barely touched).
const (
	BackfillRows  = 10000
	BackfillPause = 2 * time.Second
)

// RunBackfillPages runs BackfillPagesPass until it reaches the end, every
// pause; a failed pass is tried again a minute later. Once done it waits
// for ctx, so it can run beside the other jobs of a role.
func (s *Store) RunBackfillPages(ctx context.Context, rows int, pause time.Duration) error {
	for {
		done, err := s.BackfillPagesPass(ctx, rows)
		wait := pause
		switch {
		case ctx.Err() != nil:
			return nil
		case err != nil:
			slog.Warn("analysis: page backfill pass failed, trying again in a minute", "err", err)
			wait = time.Minute
		case done:
			slog.Info("analysis: page backfill done")
			<-ctx.Done()
			return nil
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}

// Backfills lists the backfills and their progress.
func (s *Store) Backfills(ctx context.Context) ([]Backfill, error) {
	rows, err := s.DB.Query(ctx, `SELECT name, scanned, updated, started_at, last_at, finished_at FROM analysis.backfills ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Backfill
	for rows.Next() {
		var b Backfill
		var last, finished *time.Time
		if err := rows.Scan(&b.Name, &b.Scanned, &b.Updated, &b.StartedAt, &last, &finished); err != nil {
			return nil, err
		}
		if last != nil {
			b.LastAt = *last
		}
		if finished != nil {
			b.FinishedAt = *finished
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
