package store

import (
	"bytes"
	"context"
	"encoding/hex"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
)

// Plan says how an item is queued.
type Plan struct {
	Route    string
	Lane     string
	Priority int
}

// Planner decides an item's route, lane and priority.
type Planner func(it *core.Item) Plan

// IngestStats counts what an ingest batch did.
type IngestStats struct {
	New, Edited, Unchanged, Skipped, Deleted, Redacted, Suppressed int
}

type indexRow struct {
	createdAt time.Time
	textHash  []byte
	state     string
	visible   bool
}

// Ingest stores a batch of items: new items are inserted and queued (or
// marked skipped when they have no text), edits update the text and are
// queued again, unchanged re-scrapes write nothing, deletions are marked
// and redactions remove the text and everything derived from it. Items of
// erased subjects are dropped.
func (s *Store) Ingest(ctx context.Context, items []*core.Item, plan Planner) (IngestStats, error) {
	var st IngestStats
	items = latest(items)
	if len(items) == 0 {
		return st, nil
	}
	months := make([]time.Time, 0, len(items))
	for _, it := range items {
		months = append(months, it.CreatedAt)
	}
	if err := s.EnsureMonths(ctx, months...); err != nil {
		return st, err
	}
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		st = IngestStats{}
		erased, err := erasedSubjects(ctx, tx, items)
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(items))
		for _, it := range items {
			ids = append(ids, it.ContentID)
		}
		existing := map[string]indexRow{}
		rows, err := tx.Query(ctx, `SELECT content_id, created_at, text_hash, state, visible FROM analysis.content_index
			WHERE content_id = ANY($1) FOR UPDATE`, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var r indexRow
			if err := rows.Scan(&id, &r.createdAt, &r.textHash, &r.state, &r.visible); err != nil {
				rows.Close()
				return err
			}
			existing[id] = r
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		b := &pgx.Batch{}
		var redact []itemKey
		now := time.Now().UTC()
		for _, it := range items {
			if erased["author:"+hex.EncodeToString(it.AuthorKey)] && len(it.AuthorKey) > 0 || erased["source:"+it.SourceID] {
				st.Suppressed++
				continue
			}
			ex, seen := existing[it.ContentID]
			switch it.Status {
			case core.StatusRedacted:
				if seen {
					redact = append(redact, itemKey{ex.createdAt, it.ContentID})
					st.Redacted++
				}
				continue
			case core.StatusDeleted:
				if seen && ex.visible {
					b.Queue(`UPDATE analysis.content_items SET status = 'deleted', last_seen_at = $3
						WHERE created_at = $1 AND content_id = $2 AND status = 'visible'`, ex.createdAt, it.ContentID, now)
					b.Queue(`UPDATE analysis.content_index SET visible = false, updated_at = now() WHERE content_id = $1`, it.ContentID)
					st.Deleted++
				}
				continue
			}
			hash := it.InputHash()
			analyzable := it.Analyzable()
			state, skip := "queued", (*string)(nil)
			if !analyzable {
				state = "skipped"
				r := "no_text"
				skip = &r
			}
			switch {
			case seen && bytes.Equal(ex.textHash, hash):
				if !ex.visible {
					// Seen again after a deletion: visible again.
					b.Queue(`UPDATE analysis.content_items SET status = 'visible', last_seen_at = $3
						WHERE created_at = $1 AND content_id = $2 AND status = 'deleted'`, ex.createdAt, it.ContentID, now)
					b.Queue(`UPDATE analysis.content_index SET visible = true, updated_at = now() WHERE content_id = $1`, it.ContentID)
				}
				st.Unchanged++
				continue
			case seen:
				b.Queue(`UPDATE analysis.content_items SET text = $3, media_ids = $4, hashtags = $5, status = 'visible',
					skip_reason = $6, edit_count = edit_count + 1, last_seen_at = $7
					WHERE created_at = $1 AND content_id = $2`,
					ex.createdAt, it.ContentID, nilIfEmpty(it.Text), it.MediaIDs, it.Hashtags, skip, now)
				b.Queue(`UPDATE analysis.content_index SET text_hash = $2, state = $3, visible = true, updated_at = now() WHERE content_id = $1`,
					it.ContentID, hash, state)
				it.CreatedAt = ex.createdAt
				st.Edited++
			default:
				b.Queue(`INSERT INTO analysis.content_items (created_at, content_id, platform, kind, source_id, source_name,
					thread_id, parent_id, author_key, author_is_source, text, lang_hint, hashtags, media_ids, status, skip_reason,
					first_seen_at, last_seen_at)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'visible',$15,$16,$16)
					ON CONFLICT (created_at, content_id) DO NOTHING`,
					it.CreatedAt, it.ContentID, it.Platform, it.Kind, it.SourceID, nilIfEmpty(it.SourceName),
					nilIfEmpty(it.ThreadID), nilIfEmpty(it.ParentID), nilIfEmptyBytes(it.AuthorKey), it.AuthorIsSource,
					nilIfEmpty(it.Text), nilIfEmpty(it.LangHint), it.Hashtags, it.MediaIDs, skip, now)
				b.Queue(`INSERT INTO analysis.content_index (content_id, created_at, text_hash, state) VALUES ($1,$2,$3,$4)
					ON CONFLICT (content_id) DO NOTHING`, it.ContentID, it.CreatedAt, hash, state)
				st.New++
			}
			if !analyzable {
				st.Skipped++
				continue
			}
			p := plan(it)
			reason := "new"
			if seen {
				reason = "edit"
			}
			b.Queue(`INSERT INTO analysis.analysis_queue (content_id, route, created_at, thread_id, lane, priority, reason)
				VALUES ($1,$2,$3,$4,$5,$6,$7)
				ON CONFLICT (content_id, route) DO UPDATE SET lane = EXCLUDED.lane,
					priority = LEAST(analysis.analysis_queue.priority, EXCLUDED.priority), reason = EXCLUDED.reason,
					not_before = now(), lease_until = NULL, request_id = NULL, attempts = 0, enqueued_at = now()`,
				it.ContentID, p.Route, it.CreatedAt, nilIfEmpty(it.ThreadID), p.Lane, p.Priority, reason)
		}
		if b.Len() > 0 {
			if err := tx.SendBatch(ctx, b).Close(); err != nil {
				return err
			}
		}
		if len(redact) > 0 {
			if _, err := eraseItems(ctx, tx, s, redact, true); err != nil {
				return err
			}
		}
		return nil
	})
	return st, err
}

// latest keeps the most recently observed record per content id.
func latest(items []*core.Item) []*core.Item {
	idx := map[string]int{}
	out := make([]*core.Item, 0, len(items))
	for _, it := range items {
		if it == nil || it.ContentID == "" || it.CreatedAt.IsZero() {
			continue
		}
		if i, ok := idx[it.ContentID]; ok {
			if !it.ObservedAt.Before(out[i].ObservedAt) {
				out[i] = it
			}
			continue
		}
		idx[it.ContentID] = len(out)
		out = append(out, it)
	}
	return out
}

func erasedSubjects(ctx context.Context, tx pgx.Tx, items []*core.Item) (map[string]bool, error) {
	var keys, sources []string
	for _, it := range items {
		if len(it.AuthorKey) > 0 {
			keys = append(keys, hex.EncodeToString(it.AuthorKey))
		}
		sources = append(sources, it.SourceID)
	}
	rows, err := tx.Query(ctx, `SELECT subject_type, subject_key FROM analysis.erased_subjects
		WHERE (subject_type = 'author' AND subject_key = ANY($1)) OR (subject_type = 'source' AND subject_key = ANY($2))`, keys, sources)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var t, k string
		if err := rows.Scan(&t, &k); err != nil {
			return nil, err
		}
		out[t+":"+k] = true
	}
	return out, rows.Err()
}

type itemKey struct {
	createdAt time.Time
	contentID string
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nilIfEmptyBytes(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return b
}
