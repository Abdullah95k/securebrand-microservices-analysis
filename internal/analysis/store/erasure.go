package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/adapter"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
)

// Erase deletes everything the analysis service holds about an author or a
// source: the text of their items, analyses, entity mentions, media
// analyses, queue entries and media jobs. The subject is recorded so items
// and results arriving later are dropped. Idempotent: an erasure already
// done is not repeated. Media files still pending are acknowledged so the
// collector deletes them.
func (s *Store) Erase(ctx context.Context, e core.Erasure, consumer string) (map[string]int64, error) {
	stats := map[string]int64{}
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var done bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM analysis.erasures WHERE request_id = $1 AND done_at IS NOT NULL)`,
			e.RequestID).Scan(&done); err != nil {
			return err
		}
		if done {
			stats["already_done"] = 1
			return nil
		}
		key := adapter.SubjectKey(&e)
		if _, err := tx.Exec(ctx, `INSERT INTO analysis.erased_subjects (subject_type, subject_key) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			e.Subject, key); err != nil {
			return err
		}
		var where string
		var arg any
		switch e.Subject {
		case "author":
			where, arg = "author_key = $1", e.AuthorKey
		case "source":
			where, arg = "source_id = $1", e.SourceID
		default:
			return fmt.Errorf("erasure: unknown subject %q", e.Subject)
		}
		rows, err := tx.Query(ctx, `SELECT created_at, content_id FROM analysis.content_items WHERE `+where, arg)
		if err != nil {
			return err
		}
		var keys []itemKey
		for rows.Next() {
			var k itemKey
			if err := rows.Scan(&k.createdAt, &k.contentID); err != nil {
				rows.Close()
				return err
			}
			keys = append(keys, k)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		// A source erasure deletes the items; an author erasure keeps an
		// empty row (redacted) so a re-scrape cannot bring the text back
		// even before the suppression list is checked.
		n, err := eraseItems(ctx, tx, s, keys, e.Subject == "author")
		if err != nil {
			return err
		}
		for k, v := range n {
			stats[k] = v
		}
		if e.Subject == "source" {
			// Media of the source: analyses, mention rows and pending jobs.
			tag, err := tx.Exec(ctx, `DELETE FROM analysis.entity_mentions WHERE source_id = $1 AND content_id LIKE 'media:%'`, e.SourceID)
			if err != nil {
				return err
			}
			stats["media_mentions"] = tag.RowsAffected()
			tag, err = tx.Exec(ctx, `DELETE FROM analysis.media_analyses WHERE source_id = $1`, e.SourceID)
			if err != nil {
				return err
			}
			stats["media_analyses"] = tag.RowsAffected()
			if err := s.ackPendingMedia(ctx, tx, `source_id = $1`, e.SourceID, consumer); err != nil {
				return err
			}
			for _, t := range []string{"rollup_topic_daily", "rollup_source_daily", "rollup_entity_daily"} {
				tag, err := tx.Exec(ctx, `DELETE FROM analysis.`+t+` WHERE source_id = $1`, e.SourceID)
				if err != nil {
					return err
				}
				stats[t] = tag.RowsAffected()
			}
			if _, err := tx.Exec(ctx, `DELETE FROM analysis.client_sources WHERE source_id = $1`, e.SourceID); err != nil {
				return err
			}
		} else {
			ids := make([]string, len(keys))
			for i, k := range keys {
				ids[i] = k.contentID
			}
			tag, err := tx.Exec(ctx, `DELETE FROM analysis.media_analyses WHERE owner_content_id = ANY($1)`, ids)
			if err != nil {
				return err
			}
			stats["media_analyses"] = tag.RowsAffected()
			if err := s.ackPendingMedia(ctx, tx, `owner_content_id = ANY($1)`, ids, consumer); err != nil {
				return err
			}
		}
		sb, _ := json.Marshal(stats)
		_, err = tx.Exec(ctx, `INSERT INTO analysis.erasures (request_id, subject, done_at, stats) VALUES ($1, $2, now(), $3)
			ON CONFLICT (request_id) DO UPDATE SET done_at = now(), stats = EXCLUDED.stats`, e.RequestID, e.Subject, sb)
		return err
	})
	return stats, err
}

// eraseItems removes the analysis data of items. With keepRow the content
// row stays, redacted (text and author removed); otherwise it is deleted.
// Affected rollup days are marked dirty.
func eraseItems(ctx context.Context, tx pgx.Tx, s *Store, keys []itemKey, keepRow bool) (map[string]int64, error) {
	stats := map[string]int64{}
	if len(keys) == 0 {
		return stats, nil
	}
	ats := make([]time.Time, len(keys))
	ids := make([]string, len(keys))
	days := map[time.Time]bool{}
	for i, k := range keys {
		ats[i], ids[i] = k.createdAt, k.contentID
		days[s.Day(k.createdAt)] = true
	}
	using := `USING (SELECT * FROM unnest($1::timestamptz[], $2::text[]) AS k(created_at, content_id)) k
		WHERE t.created_at = k.created_at AND t.content_id = k.content_id`
	for _, q := range []struct{ name, sql string }{
		{"mentions", `DELETE FROM analysis.entity_mentions t ` + using},
		{"analyses", `DELETE FROM analysis.analyses t ` + using},
	} {
		tag, err := tx.Exec(ctx, q.sql, ats, ids)
		if err != nil {
			return nil, fmt.Errorf("erase %s: %w", q.name, err)
		}
		stats[q.name] = tag.RowsAffected()
	}
	var tag interface{ RowsAffected() int64 }
	var err error
	if keepRow {
		tag, err = tx.Exec(ctx, `UPDATE analysis.content_items t SET text = NULL, author_key = NULL, hashtags = NULL, media_ids = NULL,
			status = 'redacted' FROM unnest($1::timestamptz[], $2::text[]) AS k(created_at, content_id)
			WHERE t.created_at = k.created_at AND t.content_id = k.content_id`, ats, ids)
	} else {
		tag, err = tx.Exec(ctx, `DELETE FROM analysis.content_items t `+using, ats, ids)
	}
	if err != nil {
		return nil, fmt.Errorf("erase items: %w", err)
	}
	stats["items"] = tag.RowsAffected()
	if keepRow {
		_, err = tx.Exec(ctx, `UPDATE analysis.content_index SET state = 'redacted', analyzed_hash = NULL, analyzed_route = NULL,
			updated_at = now() WHERE content_id = ANY($1)`, ids)
	} else {
		_, err = tx.Exec(ctx, `DELETE FROM analysis.content_index WHERE content_id = ANY($1)`, ids)
	}
	if err != nil {
		return nil, err
	}
	qtag, err := tx.Exec(ctx, `DELETE FROM analysis.analysis_queue WHERE content_id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	stats["queued"] = qtag.RowsAffected()
	if _, err := tx.Exec(ctx, `DELETE FROM analysis.dead_letters WHERE content_id = ANY($1)`, ids); err != nil {
		return nil, err
	}
	for d := range days {
		if _, err := tx.Exec(ctx, `INSERT INTO analysis.rollup_dirty (day) VALUES ($1) ON CONFLICT (day) DO UPDATE SET marked_at = now()`, d); err != nil {
			return nil, err
		}
	}
	return stats, nil
}

// ackPendingMedia acknowledges and closes pending media jobs matching where.
func (s *Store) ackPendingMedia(ctx context.Context, tx pgx.Tx, where string, arg any, consumer string) error {
	rows, err := tx.Query(ctx, `UPDATE analysis.media_jobs SET state = 'skipped', last_error = 'erased', done_at = now(), lease_until = NULL
		WHERE state = 'pending' AND `+where+` RETURNING media_id, ack_format`, arg)
	if err != nil {
		return err
	}
	var jobs []core.MediaJob
	for rows.Next() {
		var j core.MediaJob
		if err := rows.Scan(&j.MediaID, &j.AckFormat); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range jobs {
		if err := ackTx(ctx, tx, &jobs[i], consumer, "processed", "erased subject"); err != nil {
			return err
		}
	}
	return nil
}
