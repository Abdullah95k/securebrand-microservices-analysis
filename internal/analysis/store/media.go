package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	analysisv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/analysis/v1"
	contentv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/content/v1"
	fbv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/fb/v1"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/adapter"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/textnorm"
	"github.com/abdullah95k/securebrand-microservices/internal/topics"
)

// InsertMediaJobs stores announced media files. A re-announcement
// (FAILED_RETRYABLE answer, collector retry) makes a finished job pending
// again only if it failed. Jobs of erased subjects are acknowledged at once
// as processed so the file is deleted.
func (s *Store) InsertMediaJobs(ctx context.Context, jobs []*core.MediaJob, consumer string) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		n = 0
		for _, j := range jobs {
			if j.MediaID == "" || j.Bucket == "" || j.Key == "" {
				continue
			}
			var erased bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM analysis.erased_subjects WHERE subject_type = 'source' AND subject_key = $1)`,
				j.SourceID).Scan(&erased); err != nil {
				return err
			}
			if erased {
				if err := ackTx(ctx, tx, j, consumer, "processed", "erased subject"); err != nil {
					return err
				}
				continue
			}
			tag, err := tx.Exec(ctx, `INSERT INTO analysis.media_jobs (media_id, owner_content_id, platform, source_id, kind, bucket, key,
				size_bytes, content_type, duration_ms, alt_text, delete_after, ack_format, state)
				VALUES ($1, NULLIF($2,''), $3, NULLIF($4,''), $5, $6, $7, $8, NULLIF($9,''), $10, NULLIF($11,''), $12, $13, 'pending')
				ON CONFLICT (media_id) DO UPDATE SET bucket = EXCLUDED.bucket, key = EXCLUDED.key, delete_after = EXCLUDED.delete_after,
					state = 'pending', tries = 0, not_before = now(), lease_until = NULL
				WHERE analysis.media_jobs.state = 'failed'`,
				j.MediaID, j.OwnerContentID, j.Platform, j.SourceID, j.Kind, j.Bucket, j.Key, j.Size, j.ContentType, j.DurationMS,
				j.AltText, nullTime(j.DeleteAfter), j.AckFormat)
			if err != nil {
				return err
			}
			n += int(tag.RowsAffected())
		}
		return nil
	})
	return n, err
}

// ClaimMediaJobs leases pending jobs, nearest deadline first.
func (s *Store) ClaimMediaJobs(ctx context.Context, limit int, lease time.Duration) ([]core.MediaJob, error) {
	rows, err := s.DB.Query(ctx, `
		WITH c AS (
			SELECT media_id FROM analysis.media_jobs
			WHERE state = 'pending' AND not_before <= now() AND (lease_until IS NULL OR lease_until < now())
			ORDER BY delete_after NULLS LAST, received_at LIMIT $1 FOR UPDATE SKIP LOCKED)
		UPDATE analysis.media_jobs m SET lease_until = now() + make_interval(secs => $2), tries = m.tries + 1
		FROM c WHERE m.media_id = c.media_id
		RETURNING m.media_id, coalesce(m.owner_content_id, ''), m.platform, coalesce(m.source_id, ''), m.kind, m.bucket, m.key,
		          coalesce(m.size_bytes, 0), coalesce(m.content_type, ''), coalesce(m.duration_ms, 0), coalesce(m.alt_text, ''),
		          coalesce(m.delete_after, 'infinity'::timestamptz), m.ack_format, m.tries`, limit, lease.Seconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.MediaJob
	for rows.Next() {
		var j core.MediaJob
		var tries int
		var del time.Time
		if err := rows.Scan(&j.MediaID, &j.OwnerContentID, &j.Platform, &j.SourceID, &j.Kind, &j.Bucket, &j.Key, &j.Size,
			&j.ContentType, &j.DurationMS, &j.AltText, &del, &j.AckFormat, &tries); err != nil {
			return nil, err
		}
		if del.Year() < 9000 {
			j.DeleteAfter = del
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// MediaResult is a finished media analysis.
type MediaResult struct {
	Job        core.MediaJob
	Result     core.MediaResult
	Prov       core.Provenance
	OwnerTime  time.Time // the owner item's creation time, for mentions
	Consumer   string
	CostUSD    float64
	SkipReason string // set when the job was acknowledged without analysis
}

// FinishMedia stores a media analysis (or a policy skip), its logo and
// entity mentions, and the acknowledgement, in one transaction.
func (s *Store) FinishMedia(ctx context.Context, r MediaResult) error {
	if r.SkipReason == "" {
		if err := s.EnsureMonths(ctx, r.OwnerTime); err != nil {
			return err
		}
	}
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		j := &r.Job
		if r.SkipReason != "" {
			if _, err := tx.Exec(ctx, `UPDATE analysis.media_jobs SET state = 'skipped', last_error = $2, done_at = now(), lease_until = NULL
				WHERE media_id = $1`, j.MediaID, r.SkipReason); err != nil {
				return err
			}
			return ackTx(ctx, tx, j, r.Consumer, "processed", "")
		}
		res, err := json.Marshal(r.Result)
		if err != nil {
			return err
		}
		logos, _ := json.Marshal(r.Result.Logos)
		if _, err := tx.Exec(ctx, `INSERT INTO analysis.media_analyses (media_id, owner_content_id, platform, source_id, kind, analyzed_at,
			provider, model, prompt_version, taxonomy_version, description, text_in_image, transcript, logos, result, ad_type, legal)
			VALUES ($1, NULLIF($2,''), $3, NULLIF($4,''), $5, $6, $7, $8, $9, $10, NULLIF($11,''), NULLIF($12,''), NULLIF($13,''), $14, $15,
				NULLIF($16,''), $17)
			ON CONFLICT (media_id) DO UPDATE SET analyzed_at = EXCLUDED.analyzed_at, provider = EXCLUDED.provider, model = EXCLUDED.model,
				prompt_version = EXCLUDED.prompt_version, taxonomy_version = EXCLUDED.taxonomy_version, description = EXCLUDED.description,
				text_in_image = EXCLUDED.text_in_image, transcript = EXCLUDED.transcript, logos = EXCLUDED.logos, result = EXCLUDED.result,
				ad_type = EXCLUDED.ad_type, legal = EXCLUDED.legal`,
			j.MediaID, j.OwnerContentID, j.Platform, j.SourceID, j.Kind, r.Prov.AnalyzedAt, r.Prov.Provider, r.Prov.Model,
			r.Prov.PromptVersion, r.Prov.TaxonomyVersion, r.Result.Description, r.Result.TextInImage, r.Result.Transcript, logos, res,
			r.Result.AdType, nilIfNone(r.Result.Legal)); err != nil {
			return err
		}
		// Logos and entities become mentions of the media item itself, so
		// they count in the entity rollups with the owner's source.
		mediaContent := "media:" + j.MediaID
		// Earlier mentions of this media, whatever owner time they were
		// stored under; their days are rebuilt.
		if _, err := tx.Exec(ctx, `WITH d AS (DELETE FROM analysis.entity_mentions WHERE content_id = $1 RETURNING day)
			INSERT INTO analysis.rollup_dirty (day) SELECT DISTINCT day FROM d ON CONFLICT (day) DO UPDATE SET marked_at = now()`,
			mediaContent); err != nil {
			return err
		}
		origin := "image"
		if j.IsVideo() {
			origin = "video"
		}
		ents := mediaEntities(&r.Result, origin)
		day := s.Day(r.OwnerTime)
		for ord, e := range ents {
			if _, err := tx.Exec(ctx, `INSERT INTO analysis.entity_mentions (created_at, content_id, version, ord, day, platform, source_id, kind,
				entity_id, name, entity_type, sentiment, score, confidence, aspect, origin, topics)
				VALUES ($1,$2,1,$3,$4,$5,$6,$7,NULLIF($8,''),$9,$10,$11,$12,$13,$14,$15,$16)`,
				r.OwnerTime, mediaContent, ord, day, j.Platform, j.SourceID, origin, e.ID, e.Name, e.Type, e.Sentiment, e.Score,
				e.Confidence, e.Aspect, origin, r.Result.Topics); err != nil {
				return err
			}
		}
		if len(ents) > 0 {
			if _, err := tx.Exec(ctx, `INSERT INTO analysis.rollup_dirty (day) VALUES ($1) ON CONFLICT (day) DO UPDATE SET marked_at = now()`, day); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE analysis.media_jobs SET state = 'done', done_at = now(), lease_until = NULL, last_error = NULL
			WHERE media_id = $1`, j.MediaID); err != nil {
			return err
		}
		if err := outbox(ctx, tx, topics.AnalysisMedia, j.MediaID, mediaEvent(&r)); err != nil {
			return err
		}
		return ackTx(ctx, tx, j, r.Consumer, "processed", "")
	})
}

// mediaEntities merges logos into the entity list (a logo is a brand
// mention with the image's sentiment).
func mediaEntities(m *core.MediaResult, origin string) []core.Entity {
	var out []core.Entity
	seen := map[string]bool{}
	for _, e := range m.Entities {
		k := e.ID
		if k == "" {
			k = e.Type + ":" + e.Name
		}
		seen[k] = true
		e.Origin = origin
		out = append(out, e)
	}
	for _, l := range m.Logos {
		k := l.ID
		if k == "" {
			k = "brand:" + l.Name
		}
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, core.Entity{ID: l.ID, Name: l.Name, Type: "brand", Sentiment: m.Sentiment, Score: m.Score,
			Confidence: l.Confidence, Aspect: "general", Origin: origin})
	}
	return out
}

func mediaEvent(r *MediaResult) *analysisv1.MediaAnalysis {
	m := &r.Result
	ev := &analysisv1.MediaAnalysis{
		EventId: r.Job.MediaID + ":" + r.Prov.AnalyzedAt.UTC().Format(time.RFC3339), MediaId: r.Job.MediaID,
		OwnerContentId: r.Job.OwnerContentID, Platform: adapter.PlatformEnum(r.Job.Platform), SourceId: r.Job.SourceID,
		Kind: mediaKind(r.Job.Kind), TextInImage: m.TextInImage, Description: m.Description, Labels: m.Labels,
		PeopleCount: int32(m.PeopleCount), Topics: m.Topics, Transcript: m.Transcript, AdType: m.AdType, Legal: m.Legal,
		Sentiment: &analysisv1.Sentiment{Label: m.Sentiment, Score: float32(m.Score), Confidence: float32(m.Confidence)},
		Provenance: &analysisv1.Provenance{Provider: r.Prov.Provider, Model: r.Prov.Model, PromptVersion: r.Prov.PromptVersion,
			TaxonomyVersion: r.Prov.TaxonomyVersion, SchemaVersion: r.Prov.SchemaVersion, Route: r.Prov.Route, Lane: r.Prov.Lane,
			AnalyzedAt: timestamppb.New(r.Prov.AnalyzedAt), Purpose: "current", Fallback: r.Prov.Fallback, AnalysisVersion: 1},
	}
	for _, l := range m.Logos {
		ev.Logos = append(ev.Logos, &analysisv1.Logo{EntityId: l.ID, Name: l.Name, Confidence: float32(l.Confidence)})
	}
	for _, e := range m.Entities {
		ev.Entities = append(ev.Entities, &analysisv1.EntityMention{EntityId: e.ID, Name: e.Name, Type: e.Type, Aspect: e.Aspect,
			Origin: e.Origin, Sentiment: &analysisv1.Sentiment{Label: e.Sentiment, Score: float32(e.Score)}})
	}
	return ev
}

func mediaKind(kind string) contentv1.MediaKind {
	switch kind {
	case "video", "reel", "comment_video":
		return contentv1.MediaKind_MEDIA_KIND_VIDEO
	case "comment_gif", "gif":
		return contentv1.MediaKind_MEDIA_KIND_GIF
	case "sticker":
		return contentv1.MediaKind_MEDIA_KIND_STICKER
	case "audio":
		return contentv1.MediaKind_MEDIA_KIND_AUDIO
	}
	return contentv1.MediaKind_MEDIA_KIND_IMAGE
}

// FailMedia records a failed attempt. A permanent failure, or a retryable
// one out of tries, acknowledges the file as failed (the collector deletes
// it, or re-announces it once for FAILED_RETRYABLE); otherwise the job is
// retried with a backoff.
func (s *Store) FailMedia(ctx context.Context, j core.MediaJob, consumer, reason string, permanent bool, maxTries int) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var tries int
		if err := tx.QueryRow(ctx, `SELECT tries FROM analysis.media_jobs WHERE media_id = $1 FOR UPDATE`, j.MediaID).Scan(&tries); err != nil {
			return err
		}
		if !permanent && tries < maxTries {
			_, err := tx.Exec(ctx, `UPDATE analysis.media_jobs SET lease_until = NULL, last_error = left($2, 500),
				not_before = now() + make_interval(secs => least(1800, 30 * power(2, tries))) WHERE media_id = $1`, j.MediaID, reason)
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE analysis.media_jobs SET state = 'failed', lease_until = NULL, last_error = left($2, 500), done_at = now()
			WHERE media_id = $1`, j.MediaID, reason); err != nil {
			return err
		}
		status := "failed_permanent"
		if !permanent {
			status = "failed_retryable"
		}
		return ackTx(ctx, tx, &j, consumer, status, reason)
	})
}

// ackTx writes the acknowledgement to the outbox, in the collector's
// format.
func ackTx(ctx context.Context, tx pgx.Tx, j *core.MediaJob, consumer, status, errMsg string) error {
	now := timestamppb.Now()
	switch j.AckFormat {
	case "content":
		st := map[string]contentv1.ProcessStatus{"processed": contentv1.ProcessStatus_PROCESS_STATUS_PROCESSED,
			"failed_permanent": contentv1.ProcessStatus_PROCESS_STATUS_FAILED_PERMANENT,
			"failed_retryable": contentv1.ProcessStatus_PROCESS_STATUS_FAILED_RETRYABLE}[status]
		return outbox(ctx, tx, topics.ContentMediaProcessed, j.MediaID, &contentv1.MediaProcessed{
			EventId: j.MediaID + ":" + consumer, MediaId: j.MediaID, Consumer: consumer, Status: st, ProcessedAt: now, Error: cutErr(errMsg)})
	default:
		st := map[string]fbv1.ProcessStatus{"processed": fbv1.ProcessStatus_PROCESS_STATUS_PROCESSED,
			"failed_permanent": fbv1.ProcessStatus_PROCESS_STATUS_FAILED_PERMANENT,
			"failed_retryable": fbv1.ProcessStatus_PROCESS_STATUS_FAILED_RETRYABLE}[status]
		return outbox(ctx, tx, topics.MediaProcessd, j.MediaID, &fbv1.MediaProcessed{
			EventId: j.MediaID + ":" + consumer, MediaId: j.MediaID, Consumer: consumer, Status: st, ProcessedAt: now, Error: cutErr(errMsg)})
	}
}

func cutErr(s string) string {
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// OwnerContext returns the owner item's creation time, text and tagged
// spans for a media job (zero values when the owner is not stored).
func (s *Store) OwnerContext(ctx context.Context, ownerID string) (time.Time, string, []textnorm.Span, error) {
	items, err := s.ItemsByID(ctx, []string{ownerID})
	if err != nil {
		return time.Time{}, "", nil, fmt.Errorf("owner: %w", err)
	}
	it, ok := items[ownerID]
	if !ok {
		return time.Time{}, "", nil, nil
	}
	return it.CreatedAt, it.Text, it.RemoveSpans, nil
}

// TTLAtRisk counts pending media jobs within margin of their deletion.
func (s *Store) TTLAtRisk(ctx context.Context, margin time.Duration) (int64, error) {
	var n int64
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM analysis.media_jobs WHERE state = 'pending'
		AND delete_after < now() + make_interval(secs => $1)`, margin.Seconds()).Scan(&n)
	return n, err
}

// ReleaseMedia returns a claimed job to the queue without counting a try.
func (s *Store) ReleaseMedia(ctx context.Context, mediaID string, delay time.Duration) error {
	_, err := s.DB.Exec(ctx, `UPDATE analysis.media_jobs SET lease_until = NULL, tries = greatest(tries - 1, 0),
		not_before = now() + make_interval(secs => $2) WHERE media_id = $1`, mediaID, delay.Seconds())
	return err
}
