package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	analysisv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/analysis/v1"
	contentv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/content/v1"
	mediav1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/media/v1"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/textnorm"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/topics"
	fbv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/fb/v1"
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
				if err := ackTx(ctx, tx, j, consumer, "skipped", "erased_subject", "erased subject"); err != nil {
					return err
				}
				continue
			}
			tag, err := tx.Exec(ctx, `INSERT INTO analysis.media_jobs (media_id, owner_content_id, platform, source_id, kind, bucket, key,
				size_bytes, content_type, duration_ms, alt_text, delete_after, ack_format, state, role, sha256, attempt)
				VALUES ($1, NULLIF($2,''), $3, NULLIF($4,''), $5, $6, $7, $8, NULLIF($9,''), $10, NULLIF($11,''), $12, $13, 'pending',
					NULLIF($14,''), NULLIF($15,''), $16)
				ON CONFLICT (media_id) DO UPDATE SET bucket = EXCLUDED.bucket, key = EXCLUDED.key, delete_after = EXCLUDED.delete_after,
					sha256 = EXCLUDED.sha256, attempt = EXCLUDED.attempt,
					state = 'pending', tries = 0, not_before = now(), lease_until = NULL
				WHERE analysis.media_jobs.state = 'failed'`,
				j.MediaID, j.OwnerContentID, j.Platform, j.SourceID, j.Kind, j.Bucket, j.Key, j.Size, j.ContentType, j.DurationMS,
				j.AltText, nullTime(j.DeleteAfter), j.AckFormat, j.Role, j.Sha256, j.Attempt)
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
		          coalesce(m.delete_after, 'infinity'::timestamptz), m.ack_format, m.tries, coalesce(m.role, ''), coalesce(m.sha256, ''),
		          m.attempt`, limit, lease.Seconds())
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
			&j.ContentType, &j.DurationMS, &j.AltText, &del, &j.AckFormat, &tries, &j.Role, &j.Sha256, &j.Attempt); err != nil {
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
	Job       core.MediaJob
	Result    core.MediaResult
	Prov      core.Provenance
	OwnerTime time.Time // the owner item's creation time, for mentions
	Consumer  string
	CostUSD   float64
	// SkipReason, set when the job is acknowledged without analysis, is a
	// short code ("policy:sticker/attachment", "route_disabled", "refused").
	SkipReason string
	// ReusedFrom is the media whose analysis of the identical file (same
	// SHA-256) this result copies; no provider was called.
	ReusedFrom string
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
			return ackTx(ctx, tx, j, r.Consumer, "skipped", r.SkipReason, "")
		}
		res, err := json.Marshal(r.Result)
		if err != nil {
			return err
		}
		logos, _ := json.Marshal(r.Result.Logos)
		if _, err := tx.Exec(ctx, `INSERT INTO analysis.media_analyses (media_id, owner_content_id, platform, source_id, kind, analyzed_at,
			provider, model, prompt_version, taxonomy_version, description, text_in_image, transcript, logos, result, ad_type, legal,
			sha256, reused_from)
			VALUES ($1, NULLIF($2,''), $3, NULLIF($4,''), $5, $6, $7, $8, $9, $10, NULLIF($11,''), NULLIF($12,''), NULLIF($13,''), $14, $15,
				NULLIF($16,''), $17, NULLIF($18,''), NULLIF($19,''))
			ON CONFLICT (media_id) DO UPDATE SET analyzed_at = EXCLUDED.analyzed_at, provider = EXCLUDED.provider, model = EXCLUDED.model,
				prompt_version = EXCLUDED.prompt_version, taxonomy_version = EXCLUDED.taxonomy_version, description = EXCLUDED.description,
				text_in_image = EXCLUDED.text_in_image, transcript = EXCLUDED.transcript, logos = EXCLUDED.logos, result = EXCLUDED.result,
				ad_type = EXCLUDED.ad_type, legal = EXCLUDED.legal, sha256 = EXCLUDED.sha256, reused_from = EXCLUDED.reused_from`,
			j.MediaID, j.OwnerContentID, j.Platform, j.SourceID, j.Kind, r.Prov.AnalyzedAt, r.Prov.Provider, r.Prov.Model,
			r.Prov.PromptVersion, r.Prov.TaxonomyVersion, r.Result.Description, r.Result.TextInImage, r.Result.Transcript, logos, res,
			r.Result.AdType, nilIfNone(r.Result.Legal), j.Sha256, r.ReusedFrom); err != nil {
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
		// Published only for media announced through the contract; results
		// of fbs's own media stay in Postgres.
		if j.AckFormat == core.AckContract {
			if err := outbox(ctx, tx, topics.AnalysisMedia, j.MediaID, mediaEvent(&r)); err != nil {
				return err
			}
		}
		return ackTx(ctx, tx, j, r.Consumer, "processed", "", "")
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

// ContractConsumer is sb-analysis's name in the contract's registry
// (registry/platforms.yaml, consumers): what its acknowledgements on
// content.media.processed.v1 carry, and what producers list in
// required_consumers.
const ContractConsumer = "sb-analysis"

func mediaEvent(r *MediaResult) *analysisv1.MediaAnalysis {
	m := &r.Result
	ev := &analysisv1.MediaAnalysis{
		EventId: r.Job.MediaID + ":" + r.Prov.AnalyzedAt.UTC().Format(time.RFC3339Nano), MediaId: r.Job.MediaID,
		OwnerId: r.Job.OwnerContentID, OwnerKind: ownerKind(r.Job.OwnerContentID), Platform: core.PlatformCode(r.Job.MediaID),
		AccountId: r.Job.SourceID, Type: mediaType(r.Job.Kind),
		TextInImage: m.TextInImage, Description: m.Description, Labels: m.Labels,
		PeopleCount: int32(m.PeopleCount), Topics: m.Topics, Transcript: m.Transcript, TranscriptLang: m.TranscriptLang,
		TranscriptSource: m.TranscriptSource, AdType: m.AdType, Legal: m.Legal,
		Sentiment: &analysisv1.Sentiment{Label: m.Sentiment, Score: float32(m.Score), Confidence: float32(m.Confidence)},
		Provenance: &analysisv1.Provenance{Producer: Producer, ProducerVersion: ProducerVersion, Provider: r.Prov.Provider,
			Model: r.Prov.Model, PromptVersion: r.Prov.PromptVersion, TaxonomyVersion: r.Prov.TaxonomyVersion,
			SchemaVersion: r.Prov.SchemaVersion, Route: r.Prov.Route, Lane: r.Prov.Lane,
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

// ownerKind is the entity kind of a contract ID ("post", "comment", ...).
func ownerKind(id string) string {
	parts := strings.SplitN(id, ":", 3)
	if len(parts) == 3 {
		return parts[1]
	}
	return ""
}

func mediaType(kind string) contentv1.MediaType {
	switch kind {
	case "video", "reel", "comment_video":
		return contentv1.MediaType_MEDIA_TYPE_VIDEO
	case "comment_gif", "gif":
		return contentv1.MediaType_MEDIA_TYPE_GIF
	case "sticker":
		return contentv1.MediaType_MEDIA_TYPE_STICKER
	case "audio":
		return contentv1.MediaType_MEDIA_TYPE_AUDIO
	case "document":
		return contentv1.MediaType_MEDIA_TYPE_DOCUMENT
	}
	return contentv1.MediaType_MEDIA_TYPE_IMAGE
}

// FailMedia records a failed attempt: code is a short reason ("too_large",
// "undecodable", "gone"...), detail the error. A permanent failure, or a
// retryable one out of tries, acknowledges the file as failed (the
// collector deletes it, or re-announces it for FAILED_RETRYABLE);
// otherwise the job is retried with a backoff. A job whose file was
// reported deleted meanwhile is left as it is.
func (s *Store) FailMedia(ctx context.Context, j core.MediaJob, consumer, code, detail string, permanent bool, maxTries int) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var tries int
		var state string
		if err := tx.QueryRow(ctx, `SELECT tries, state FROM analysis.media_jobs WHERE media_id = $1 FOR UPDATE`, j.MediaID).
			Scan(&tries, &state); err != nil {
			return err
		}
		if state != "pending" {
			return nil
		}
		if !permanent && tries < maxTries {
			_, err := tx.Exec(ctx, `UPDATE analysis.media_jobs SET lease_until = NULL, last_error = left($2, 500),
				not_before = now() + make_interval(secs => least(1800, 30 * power(2, tries))) WHERE media_id = $1`, j.MediaID, detail)
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE analysis.media_jobs SET state = 'failed', lease_until = NULL, last_error = left($2, 500), done_at = now()
			WHERE media_id = $1`, j.MediaID, detail); err != nil {
			return err
		}
		status := "failed_permanent"
		if !permanent {
			status = "failed_retryable"
		}
		return ackTx(ctx, tx, &j, consumer, status, code, detail)
	})
}

// ackTx writes the acknowledgement to the outbox, in the announcer's
// format. status is processed, skipped, failed_permanent or
// failed_retryable; reason a short code (skips and failures), detail the
// error. fbs's format has no SKIPPED status and no reason: a skip is
// PROCESSED there, as before.
func ackTx(ctx context.Context, tx pgx.Tx, j *core.MediaJob, consumer, status, reason, detail string) error {
	now := timestamppb.Now()
	if j.AckFormat == core.AckContract {
		st := map[string]mediav1.ProcessStatus{"processed": mediav1.ProcessStatus_PROCESS_STATUS_PROCESSED,
			"skipped":          mediav1.ProcessStatus_PROCESS_STATUS_SKIPPED,
			"failed_permanent": mediav1.ProcessStatus_PROCESS_STATUS_FAILED_PERMANENT,
			"failed_retryable": mediav1.ProcessStatus_PROCESS_STATUS_FAILED_RETRYABLE}[status]
		// Every acknowledgement is an event of its own: a retried file is
		// acknowledged again under a new event_id.
		return outbox(ctx, tx, topics.ContentMediaProcessed, j.MediaID, &mediav1.MediaProcessed{
			EventId: uuid.NewString(), MediaId: j.MediaID, Consumer: ContractConsumer, Status: st, ProcessedAt: now,
			Error: cutErr(detail), Platform: core.PlatformCode(j.MediaID), Reason: reason})
	}
	st := map[string]fbv1.ProcessStatus{"processed": fbv1.ProcessStatus_PROCESS_STATUS_PROCESSED,
		"skipped":          fbv1.ProcessStatus_PROCESS_STATUS_PROCESSED,
		"failed_permanent": fbv1.ProcessStatus_PROCESS_STATUS_FAILED_PERMANENT,
		"failed_retryable": fbv1.ProcessStatus_PROCESS_STATUS_FAILED_RETRYABLE}[status]
	return outbox(ctx, tx, topics.MediaProcessd, j.MediaID, &fbv1.MediaProcessed{
		EventId: j.MediaID + ":" + consumer, MediaId: j.MediaID, Consumer: consumer, Status: st, ProcessedAt: now, Error: cutErr(detail)})
}

// MediaGone marks waiting jobs whose files were deleted from storage
// (content.media.deleted.v1): there is nothing left to analyse, and no
// acknowledgement is owed. It returns how many jobs it closed.
func (s *Store) MediaGone(ctx context.Context, gone []*core.MediaGone) (int, error) {
	n := 0
	for _, g := range gone {
		tag, err := s.DB.Exec(ctx, `UPDATE analysis.media_jobs SET state = 'gone', lease_until = NULL, done_at = now(),
			last_error = 'file deleted: ' || $2 WHERE media_id = $1 AND state = 'pending'`, g.MediaID, g.Reason)
		if err != nil {
			return n, err
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// MediaBySHA returns the latest analysis of another media item with the
// same file (SHA-256), to reuse instead of analysing the file again.
func (s *Store) MediaBySHA(ctx context.Context, sha256, mediaID string) (from string, res core.MediaResult, prov core.Provenance, ok bool, err error) {
	if sha256 == "" {
		return "", res, prov, false, nil
	}
	var raw []byte
	err = s.DB.QueryRow(ctx, `SELECT media_id, result, provider, model, prompt_version, taxonomy_version, analyzed_at
		FROM analysis.media_analyses WHERE sha256 = $1 AND media_id <> $2 ORDER BY analyzed_at DESC LIMIT 1`, sha256, mediaID).
		Scan(&from, &raw, &prov.Provider, &prov.Model, &prov.PromptVersion, &prov.TaxonomyVersion, &prov.AnalyzedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", res, prov, false, nil
	}
	if err != nil {
		return "", res, prov, false, err
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", res, prov, false, fmt.Errorf("media %s: stored result: %w", from, err)
	}
	return from, res, prov, true, nil
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

// Owner is what a media job needs of the item it belongs to.
type Owner struct {
	CreatedAt time.Time
	Text      string
	Tags      []textnorm.Span
	// The post's caption track (contract posts), its language and source.
	Captions, CaptionsLang, CaptionsSource string
}

// OwnerContext returns the owner item of a media job (zero values when the
// owner is not stored).
func (s *Store) OwnerContext(ctx context.Context, ownerID string) (Owner, error) {
	items, err := s.ItemsByID(ctx, []string{ownerID})
	if err != nil {
		return Owner{}, fmt.Errorf("owner: %w", err)
	}
	it, ok := items[ownerID]
	if !ok {
		return Owner{}, nil
	}
	o := Owner{CreatedAt: it.CreatedAt, Text: it.Text, Tags: it.RemoveSpans}
	if err := s.DB.QueryRow(ctx, `SELECT coalesce(captions, ''), coalesce(captions_lang, ''), coalesce(captions_source, '')
		FROM analysis.content_items WHERE created_at = $1 AND content_id = $2`, it.CreatedAt, ownerID).
		Scan(&o.Captions, &o.CaptionsLang, &o.CaptionsSource); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Owner{}, fmt.Errorf("owner captions: %w", err)
	}
	return o, nil
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
