package store

import (
	"bytes"
	"context"
	"encoding/hex"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/textnorm"
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
	// RedactedKept counts redactions not applied: analysis.erasure.mode is
	// not "apply", so the stored text and results are kept.
	RedactedKept int
	// Adopted counts items analysed from fbs's own topics arriving for the
	// first time through the contract: their stored analysis is published,
	// with no new AI call (the cut-over, decision 17).
	Adopted int
	// NotSampled counts new comments outside analysis.comments.sample_share:
	// counted per day and platform, not stored.
	NotSampled int
	// Stale counts contract copies whose envelope.version is at or below
	// the stored one: the same reading again, or an older one (fbs's
	// version is the reading's time and only rises per item). Nothing is
	// written for them.
	Stale int
}

// sampleCount is one Iraq day and platform's comment records: received,
// in the sample, and kept outside it because they take the real-time lane.
type sampleCount struct{ received, sampled, urgent int }

type indexRow struct {
	createdAt time.Time
	textHash  []byte
	state     string
	visible   bool
	// version is the stored item's envelope.version: 0 for an item read
	// from fbs's own topics (fb.*), which carry none.
	version int64
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
		rows, err := tx.Query(ctx, `SELECT i.content_id, i.created_at, i.text_hash, i.state, i.visible, coalesce(c.content_version, 0)
			FROM analysis.content_index i
			LEFT JOIN analysis.content_items c ON c.created_at = i.created_at AND c.content_id = i.content_id
			WHERE i.content_id = ANY($1) FOR UPDATE OF i`, ids)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var r indexRow
			if err := rows.Scan(&id, &r.createdAt, &r.textHash, &r.state, &r.visible, &r.version); err != nil {
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
		var adopt []*core.Item
		now := time.Now().UTC()
		share := s.CommentShare
		if share <= 0 {
			share = 1
		}
		type dayPlatform struct {
			day      string
			platform string
		}
		counts := map[dayPlatform]*sampleCount{}
		for _, it := range items {
			if erased["author:"+hex.EncodeToString(it.AuthorKey)] && len(it.AuthorKey) > 0 || erased["source:"+it.SourceID] {
				st.Suppressed++
				continue
			}
			ex, seen := existing[it.ContentID]
			switch it.Status {
			case core.StatusRedacted:
				switch {
				case seen && s.DeletesOnErasure():
					redact = append(redact, itemKey{ex.createdAt, it.ContentID})
					st.Redacted++
				case seen:
					st.RedactedKept++
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
			if seen && it.FromContract() && ex.version > 0 && it.Version <= ex.version {
				// The same reading again, or an older one (a redelivery, or a
				// backfill of an earlier scrape, such as the pilot's): its
				// text must not replace the newer one stored, nor be paid
				// for again. Items stored from fbs's own topics (version 0)
				// have no version to compare and go on as before.
				st.Stale++
				continue
			}
			if it.Kind != core.KindPost {
				// The owner's D-053: a fixed share of comments, by ID. A
				// comment outside it is counted, and stored only if it is
				// stored already (its edits keep it current) or takes the
				// real-time lane.
				k := dayPlatform{now.In(s.Loc).Format(time.DateOnly), it.Platform}
				c := counts[k]
				if c == nil {
					c = &sampleCount{}
					counts[k] = c
				}
				c.received++
				switch {
				case core.InSample(it.ContentID, share):
					c.sampled++
				case seen:
				case plan(it).Priority == core.PriorityUrgent:
					c.urgent++
				default:
					st.NotSampled++
					continue
				}
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
			case seen && it.FromContract() && ex.version == 0 && ex.state == "analyzed" && analyzable:
				// The cut-over (decision 17): the first contract version of an
				// item already analysed from fbs's own topics takes the
				// contract's fields and is not analysed again, whatever its
				// hash (the two paths word posts and name media slightly
				// differently); its stored analysis is published on
				// analysis.result.v1 below. Later versions are edits as usual.
				b.Queue(`UPDATE analysis.content_items SET text = $3, media_ids = $4, hashtags = $5, status = 'visible',
					skip_reason = NULL, last_seen_at = $6, tag_spans = $7, content_version = GREATEST(content_version, $8), depth = $9,
					author_id = coalesce($10, author_id), captions = coalesce($11, captions), captions_lang = coalesce($12, captions_lang),
					captions_source = coalesce($13, captions_source)
					WHERE created_at = $1 AND content_id = $2`,
					ex.createdAt, it.ContentID, nilIfEmpty(it.Text), it.MediaIDs, it.Hashtags, now, textnorm.SpansToInts(it.RemoveSpans),
					it.Version, it.Depth, nilIfEmpty(it.AuthorID), nilIfEmpty(it.Captions), nilIfEmpty(it.CaptionsLang),
					nilIfEmpty(it.CaptionsSource))
				b.Queue(`UPDATE analysis.content_index SET text_hash = $2, visible = true, updated_at = now() WHERE content_id = $1`,
					it.ContentID, hash)
				it.CreatedAt = ex.createdAt
				adopt = append(adopt, it)
				continue
			case seen && bytes.Equal(ex.textHash, hash):
				if it.FromContract() && ex.version == 0 {
					// An item first read from fbs's own topics, not analysed
					// yet, now through the contract unchanged: it takes the
					// contract's version and fields, so its result is
					// published on analysis.result.v1 when it is analysed
					// (one analysed already is adopted above).
					b.Queue(`UPDATE analysis.content_items SET content_version = GREATEST(content_version, $3), depth = $4,
						author_id = coalesce($5, author_id), last_seen_at = $6
						WHERE created_at = $1 AND content_id = $2`,
						ex.createdAt, it.ContentID, it.Version, it.Depth, nilIfEmpty(it.AuthorID), now)
				}
				if it.Captions != "" {
					// Same text, captions added or changed: kept for the
					// video's transcript.
					b.Queue(`UPDATE analysis.content_items SET captions = $3, captions_lang = $4, captions_source = $5
						WHERE created_at = $1 AND content_id = $2 AND captions IS DISTINCT FROM $3`,
						ex.createdAt, it.ContentID, it.Captions, nilIfEmpty(it.CaptionsLang), nilIfEmpty(it.CaptionsSource))
				}
				if len(it.RemoveSpans) > 0 {
					// Same text: keep the tag positions current (rows stored
					// before they were kept, or ranges the collector fixed).
					b.Queue(`UPDATE analysis.content_items SET tag_spans = $3 WHERE created_at = $1 AND content_id = $2
						AND tag_spans IS DISTINCT FROM $3`, ex.createdAt, it.ContentID, textnorm.SpansToInts(it.RemoveSpans))
				}
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
					skip_reason = $6, edit_count = edit_count + 1, last_seen_at = $7, tag_spans = $8,
					content_version = GREATEST(content_version, $9), depth = $10, author_id = coalesce($11, author_id),
					captions = coalesce($12, captions), captions_lang = coalesce($13, captions_lang),
					captions_source = coalesce($14, captions_source)
					WHERE created_at = $1 AND content_id = $2`,
					ex.createdAt, it.ContentID, nilIfEmpty(it.Text), it.MediaIDs, it.Hashtags, skip, now, textnorm.SpansToInts(it.RemoveSpans),
					it.Version, it.Depth, nilIfEmpty(it.AuthorID), nilIfEmpty(it.Captions), nilIfEmpty(it.CaptionsLang),
					nilIfEmpty(it.CaptionsSource))
				b.Queue(`UPDATE analysis.content_index SET text_hash = $2, state = $3, visible = true, updated_at = now() WHERE content_id = $1`,
					it.ContentID, hash, state)
				it.CreatedAt = ex.createdAt
				st.Edited++
			default:
				b.Queue(`INSERT INTO analysis.content_items (created_at, content_id, platform, kind, source_id, source_name,
					thread_id, parent_id, author_key, author_is_source, text, lang_hint, hashtags, media_ids, status, skip_reason,
					first_seen_at, last_seen_at, tag_spans, content_version, depth, author_id, captions, captions_lang, captions_source)
					VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,'visible',$15,$16,$16,$17,$18,$19,$20,$21,$22,$23)
					ON CONFLICT (created_at, content_id) DO NOTHING`,
					it.CreatedAt, it.ContentID, it.Platform, it.Kind, it.SourceID, nilIfEmpty(it.SourceName),
					nilIfEmpty(it.ThreadID), nilIfEmpty(it.ParentID), nilIfEmptyBytes(it.AuthorKey), it.AuthorIsSource,
					nilIfEmpty(it.Text), nilIfEmpty(it.LangHint), it.Hashtags, it.MediaIDs, skip, now, textnorm.SpansToInts(it.RemoveSpans),
					it.Version, it.Depth, nilIfEmpty(it.AuthorID), nilIfEmpty(it.Captions), nilIfEmpty(it.CaptionsLang),
					nilIfEmpty(it.CaptionsSource))
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
			b.Queue(`INSERT INTO analysis.analysis_queue (content_id, route, created_at, thread_id, source_id, lane, priority, reason)
				VALUES ($1,$2,$3,$4,$8,$5,$6,$7)
				ON CONFLICT (content_id, route) DO UPDATE SET lane = EXCLUDED.lane,
					priority = LEAST(analysis.analysis_queue.priority, EXCLUDED.priority), reason = EXCLUDED.reason,
					not_before = now(), lease_until = NULL, request_id = NULL, attempts = 0, enqueued_at = now(),
					source_id = EXCLUDED.source_id`,
				it.ContentID, p.Route, it.CreatedAt, nilIfEmpty(it.ThreadID), p.Lane, p.Priority, reason, QueueSource(*it, p.Route))
		}
		for k, c := range counts {
			b.Queue(`INSERT INTO analysis.comment_sample_daily (day, platform, received, sampled, urgent, share)
				VALUES ($1::date, $2, $3, $4, $5, $6)
				ON CONFLICT (day, platform) DO UPDATE SET received = analysis.comment_sample_daily.received + EXCLUDED.received,
					sampled = analysis.comment_sample_daily.sampled + EXCLUDED.sampled,
					urgent = analysis.comment_sample_daily.urgent + EXCLUDED.urgent, share = EXCLUDED.share`,
				k.day, k.platform, c.received, c.sampled, c.urgent, share)
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
		for _, it := range adopt {
			published, err := publishAdopted(ctx, tx, it)
			if err != nil {
				return err
			}
			if published {
				st.Adopted++
				continue
			}
			// No current analysis after all: analysed as an edit.
			p := plan(it)
			if _, err := tx.Exec(ctx, `INSERT INTO analysis.analysis_queue (content_id, route, created_at, thread_id, source_id, lane, priority, reason)
				VALUES ($1,$2,$3,$4,$7,$5,$6,'edit') ON CONFLICT (content_id, route) DO UPDATE SET not_before = now(), lease_until = NULL,
					request_id = NULL, attempts = 0, enqueued_at = now(), reason = 'edit', source_id = EXCLUDED.source_id`,
				it.ContentID, p.Route, it.CreatedAt, nilIfEmpty(it.ThreadID), p.Lane, p.Priority, QueueSource(*it, p.Route)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE analysis.content_index SET state = 'queued' WHERE content_id = $1`, it.ContentID); err != nil {
				return err
			}
			st.Edited++
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
