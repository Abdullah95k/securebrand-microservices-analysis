package store

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	analysisv1 "github.com/abdullah95k/securebrand-contracts/gen/go/securebrand/analysis/v1"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/core"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/topics"
)

// ItemResult is one item's validated analysis, ready to store.
type ItemResult struct {
	Item      core.Item
	Route     string
	InputHash []byte
	Result    core.Result
	Prov      core.Provenance
	// Audit results are stored for agreement monitoring only.
	Audit   bool
	CostUSD float64
	// FollowUps are routes to queue the item on next (escalation, audit).
	FollowUps []string
}

// RequestOutcome closes a provider request.
type RequestOutcome struct {
	ID uuid.UUID
	// Provider, Model and Fallback record who actually answered (a
	// fallback may have served a request recorded for the primary).
	Provider  string
	Model     string
	Fallback  bool
	Usage     [4]int // input, output, cache read, cache write
	CostUSD   float64
	LatencyMS int
	// Failed items go back to the queue (alone, with a backoff) or to the
	// dead letters; Reason explains why.
	Failed      []string
	Reason      string
	MaxAttempts int
}

// WriteStats counts what a write did.
type WriteStats struct {
	Written, Stale, Duplicate int
}

// WriteResults stores a request's results in one transaction: the analysis
// row (previous version no longer current), the entity mentions, the index
// state, the dirty rollup day, the outbox record for analysis.result.v1 and
// any follow-up queue entries; then closes the request and releases failed
// items. A result whose input no longer matches the item (edited or erased
// meanwhile), or that duplicates the current analysis, is dropped.
func (s *Store) WriteResults(ctx context.Context, results []ItemResult, out RequestOutcome) (WriteStats, error) {
	var st WriteStats
	months := make([]time.Time, 0, len(results))
	for _, r := range results {
		months = append(months, r.Item.CreatedAt)
	}
	if err := s.EnsureMonths(ctx, months...); err != nil {
		return st, err
	}
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, setLockTimeout); err != nil {
			return err
		}
		st = WriteStats{}
		// The reads that decide what to write go one by one; the writes of
		// every item and the request's own update go together, in one round
		// trip (on 12 Nov, a statement a round trip made a batch result of
		// about 8 items take 1.3 s, about 100 round trips).
		var w pendingWrites
		for i := range results {
			ok, dup, err := s.writeOne(ctx, tx, &results[i], out.ID, &w)
			if err != nil {
				return fmt.Errorf("write %s: %w", results[i].Item.ContentID, err)
			}
			switch {
			case ok:
				st.Written++
			case dup:
				st.Duplicate++
			default:
				st.Stale++
			}
		}
		if out.ID != uuid.Nil {
			w.queue("request", `UPDATE analysis.provider_requests SET status = 'done', completed_at = now(),
				input_tokens = $2, output_tokens = $3, cache_read_tokens = $4, cache_write_tokens = $5, cost_usd = $6,
				latency_ms = $7, error = NULLIF(left($8, 500), ''), provider = coalesce(NULLIF($9, ''), provider),
				model = coalesce(NULLIF($10, ''), model), fallback = fallback OR $11 WHERE request_id = $1`,
				out.ID, out.Usage[0], out.Usage[1], out.Usage[2], out.Usage[3], out.CostUSD, out.LatencyMS, out.Reason,
				out.Provider, out.Model, out.Fallback)
		}
		if err := w.send(ctx, tx); err != nil {
			return err
		}
		if out.ID != uuid.Nil {
			if len(out.Failed) > 0 {
				if err := releaseTx(ctx, tx, out.ID, out.Failed, out.Reason, out.MaxAttempts); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return st, err
}

// writeOne decides an item result's writes, reading what it needs, and
// queues them on w (sent once for the whole request: WriteResults).
func (s *Store) writeOne(ctx context.Context, tx pgx.Tx, r *ItemResult, req uuid.UUID, w *pendingWrites) (written, duplicate bool, err error) {
	it := &r.Item
	var (
		textHash, analyzedHash []byte
		analyzedRoute          *string
		version                int
	)
	err = tx.QueryRow(ctx, `SELECT text_hash, analyzed_hash, analyzed_route, analysis_version FROM analysis.content_index
		WHERE content_id = $1 FOR UPDATE`, it.ContentID).Scan(&textHash, &analyzedHash, &analyzedRoute, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, dequeue(w, it.ContentID, r.Route, req)
	}
	if err != nil {
		return false, false, err
	}
	if !bytes.Equal(textHash, r.InputHash) {
		// Edited meanwhile: the queue entry was reset by the edit, keep it.
		return false, false, dequeue(w, it.ContentID, r.Route, req)
	}
	if r.Audit {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM analysis.analyses WHERE created_at = $1 AND content_id = $2
			AND purpose = 'audit' AND input_hash = $3)`, it.CreatedAt, it.ContentID, r.InputHash).Scan(&exists); err != nil {
			return false, false, err
		}
		if exists {
			return false, true, dequeue(w, it.ContentID, r.Route, req)
		}
	} else if bytes.Equal(analyzedHash, r.InputHash) && analyzedRoute != nil {
		dup, err := s.duplicate(ctx, tx, r, *analyzedRoute)
		if err != nil {
			return false, false, err
		}
		if dup {
			return false, true, dequeue(w, it.ContentID, r.Route, req)
		}
	}
	version++
	res, err := json.Marshal(r.Result)
	if err != nil {
		return false, false, err
	}
	purpose := "current"
	if r.Audit {
		purpose = "audit"
	} else {
		w.queue(it.ContentID, `UPDATE analysis.analyses SET is_current = false WHERE created_at = $1 AND content_id = $2 AND is_current`,
			it.CreatedAt, it.ContentID)
	}
	p := r.Prov
	var reqID *uuid.UUID
	if req != uuid.Nil {
		reqID = &req
	}
	x := r.Result
	w.queue(it.ContentID, `INSERT INTO analysis.analyses (created_at, content_id, version, is_current, purpose, platform, source_id, kind,
		route, lane, provider, model, prompt_version, taxonomy_version, schema_version, input_hash, fallback, analyzed_at, request_id,
		lang, dialect, script, code_switch, sentiment, sentiment_score, confidence, calibrated_confidence, intent, emotion, toxicity,
		spam, sarcasm, urgent, topics, keywords, private_person_mentions, refused, result, cost_usd, ad_type, legal, author_key)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,
		        $31,$32,$33,$34,$35,$36,$37,$38,$39,NULLIF($40,''),$41,$42)`,
		it.CreatedAt, it.ContentID, version, !r.Audit, purpose, it.Platform, it.SourceID, it.Kind,
		r.Route, p.Lane, p.Provider, p.Model, p.PromptVersion, p.TaxonomyVersion, p.SchemaVersion, r.InputHash, p.Fallback, p.AnalyzedAt, reqID,
		x.Lang, x.Dialect, x.Script, x.CodeSwitch, x.Sentiment, x.Score, x.Confidence, x.CalibratedConfidence, x.Intent, x.Emotion, x.Toxicity,
		x.Spam, x.Sarcasm, x.Urgent, x.Topics, x.Keywords, x.PrivatePersonMentions, x.Refused, res, r.CostUSD,
		x.AdType, nilIfNone(x.Legal), nilIfEmptyBytes(it.AuthorKey))
	if r.Audit {
		w.queue(it.ContentID, `UPDATE analysis.content_index SET analysis_version = $2, updated_at = now() WHERE content_id = $1`,
			it.ContentID, version)
		return true, false, dequeue(w, it.ContentID, r.Route, req)
	}
	w.queue(it.ContentID, `DELETE FROM analysis.entity_mentions WHERE created_at = $1 AND content_id = $2`, it.CreatedAt, it.ContentID)
	day := s.Day(it.CreatedAt)
	for ord, e := range x.Entities {
		w.queue(it.ContentID, `INSERT INTO analysis.entity_mentions (created_at, content_id, version, ord, day, platform, source_id, kind,
			author_key, entity_id, name, entity_type, sentiment, score, confidence, aspect, origin, topics, lang, intent)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''),$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
			it.CreatedAt, it.ContentID, version, ord, day, it.Platform, it.SourceID, it.Kind, nilIfEmptyBytes(it.AuthorKey),
			e.ID, e.Name, e.Type, e.Sentiment, e.Score, e.Confidence, e.Aspect, orDefault(e.Origin, "text"), x.Topics, x.Lang, x.Intent)
	}
	w.queue(it.ContentID, `UPDATE analysis.content_index SET analysis_version = $2, analyzed_hash = $3, analyzed_route = $4,
		state = 'analyzed', updated_at = now() WHERE content_id = $1`, it.ContentID, version, r.InputHash, r.Route)
	w.queue(it.ContentID, `INSERT INTO analysis.rollup_dirty (day) VALUES ($1) ON CONFLICT (day) DO UPDATE SET marked_at = now()`, day)
	// Only items read through the platform's contract are published: their
	// results carry contract IDs and the analysed envelope.version. Results
	// of items from fbs's own topics stay in Postgres.
	if it.FromContract() {
		ev := ItemEvent(it, &x, &p, r.Route, r.InputHash, version)
		if ev.AuthorBot, err = authorBot(ctx, tx, it.Platform, it.AuthorKey); err != nil {
			return false, false, err
		}
		if err := w.outbox(it.ContentID, topics.AnalysisResult, it.ContentID, ev); err != nil {
			return false, false, err
		}
	}
	for _, fu := range r.FollowUps {
		w.queue(it.ContentID, `INSERT INTO analysis.analysis_queue (content_id, route, created_at, thread_id, source_id, lane, priority, reason)
			VALUES ($1,$2,$3,NULLIF($4,''),$7,'batch',$5,$6) ON CONFLICT (content_id, route) DO NOTHING`,
			it.ContentID, fu, it.CreatedAt, it.ThreadID, core.PriorityBackground, fu, QueueSource(*it, fu))
	}
	return true, false, dequeue(w, it.ContentID, r.Route, req)
}

// duplicate reports whether a result for an already analysed input adds
// nothing: the current analysis came from the same route, model, prompt and
// taxonomy (a re-polled batch), or it is an escalation that a late
// comment/post result must not replace. A re-analysis with another model,
// prompt or taxonomy is not a duplicate.
func (s *Store) duplicate(ctx context.Context, tx pgx.Tx, r *ItemResult, curRoute string) (bool, error) {
	if curRoute == core.RouteEscalation && (r.Route == core.RouteComment || r.Route == core.RoutePost) {
		return true, nil
	}
	if curRoute != r.Route {
		return false, nil
	}
	var model, promptV, taxV string
	err := tx.QueryRow(ctx, `SELECT model, prompt_version, taxonomy_version FROM analysis.analyses
		WHERE created_at = $1 AND content_id = $2 AND is_current`, r.Item.CreatedAt, r.Item.ContentID).Scan(&model, &promptV, &taxV)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return model == r.Prov.Model && promptV == r.Prov.PromptVersion && taxV == r.Prov.TaxonomyVersion, nil
}

// dequeue queues the removal of the item's queue entry of this request. An
// entry reset by an edit (request id cleared) stays.
func dequeue(w *pendingWrites, id, route string, req uuid.UUID) error {
	if req == uuid.Nil {
		w.queue(id, `DELETE FROM analysis.analysis_queue WHERE content_id = $1 AND route = $2 AND request_id IS NULL`, id, route)
		return nil
	}
	w.queue(id, `DELETE FROM analysis.analysis_queue WHERE content_id = $1 AND route = $2 AND request_id = $3`, id, route, req)
	return nil
}

// pendingWrites are writes queued to go to the database together, in one
// round trip, each labelled with what it is for (its item) for errors.
type pendingWrites struct {
	b      pgx.Batch
	labels []string
}

func (w *pendingWrites) queue(label, sql string, args ...any) {
	w.b.Queue(sql, args...)
	w.labels = append(w.labels, label)
}

// outbox queues a record for the outbox (as outbox does).
func (w *pendingWrites) outbox(label, topic, key string, msg proto.Message) error {
	b, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	w.queue(label, `INSERT INTO analysis.outbox (topic, key, type, payload) VALUES ($1, $2, $3, $4)`,
		topic, key, string(proto.MessageName(msg)), b)
	return nil
}

// send runs the queued writes in order, in one round trip, and returns
// the first failure, naming what it was for.
func (w *pendingWrites) send(ctx context.Context, tx pgx.Tx) error {
	if w.b.Len() == 0 {
		return nil
	}
	br := tx.SendBatch(ctx, &w.b)
	for _, label := range w.labels {
		if _, err := br.Exec(); err != nil {
			_ = br.Close()
			return fmt.Errorf("write %s: %w", label, err)
		}
	}
	return br.Close()
}

// Producer names sb-analysis in every record's provenance; ProducerVersion
// is its build (set at link time with -X).
const Producer = "sb-analysis"

var ProducerVersion = "dev"

// ItemEvent is the analysis.result.v1 record of a result, in the contract's
// securebrand.analysis.v1: the item's contract IDs, the envelope.version
// that was analysed, why it took its lane and the tracked entities that
// matched (for the alert engine).
func ItemEvent(it *core.Item, x *core.Result, p *core.Provenance, route string, inputHash []byte, version int) *analysisv1.ItemAnalysis {
	kind := "post"
	if it.Kind != core.KindPost {
		kind = "comment"
	}
	ev := &analysisv1.ItemAnalysis{
		EventId:        fmt.Sprintf("%s:%d", it.ContentID, version),
		ContentId:      it.ContentID,
		Platform:       core.PlatformCode(it.ContentID),
		Kind:           kind,
		AccountId:      it.SourceID,
		AuthorId:       it.AuthorID,
		ContentVersion: it.Version,
		CreatedAt:      timestamppb.New(it.CreatedAt),
		Lang:           x.Lang, Dialect: x.Dialect, Script: x.Script, CodeSwitch: x.CodeSwitch,
		Sentiment: &analysisv1.Sentiment{Label: x.Sentiment, Score: float32(x.Score), Confidence: float32(x.Confidence),
			CalibratedConfidence: float32(x.CalibratedConfidence)},
		Topics: x.Topics, Keywords: x.Keywords, Intent: x.Intent, Emotion: x.Emotion,
		Toxicity: &analysisv1.Toxicity{Level: x.Toxicity, Hate: x.Hate, Threat: x.Threat, Harassment: x.Harassment},
		Spam:     x.Spam, Sarcasm: x.Sarcasm, Urgent: x.Urgent,
		PrivatePersonMentions: int32(x.PrivatePersonMentions),
		Refused:               x.Refused,
		AdType:                x.AdType,
		Legal:                 x.Legal,
		Routing:               orDefault(p.Routing, "normal"),
		MatchedEntityIds:      p.MatchedEntityIDs,
		Provenance: &analysisv1.Provenance{Producer: Producer, ProducerVersion: ProducerVersion, Provider: p.Provider,
			Model: p.Model, PromptVersion: p.PromptVersion, TaxonomyVersion: p.TaxonomyVersion, SchemaVersion: p.SchemaVersion,
			Route: route, Lane: p.Lane, AnalyzedAt: timestamppb.New(p.AnalyzedAt), InputHash: hex.EncodeToString(inputHash),
			AnalysisVersion: uint32(version), Purpose: "current", Fallback: p.Fallback},
	}
	if kind == "comment" {
		ev.PostId = it.ThreadID
		ev.Depth = int32(it.Depth)
		if it.Depth > 0 {
			ev.ParentCommentId = it.ParentID
		}
	}
	for _, e := range x.Entities {
		ev.Entities = append(ev.Entities, &analysisv1.EntityMention{EntityId: e.ID, Name: e.Name, Type: e.Type, Aspect: e.Aspect,
			Origin: orDefault(e.Origin, "text"), Sentiment: &analysisv1.Sentiment{Label: e.Sentiment, Score: float32(e.Score)}})
	}
	return ev
}

func outbox(ctx context.Context, tx pgx.Tx, topic, key string, msg proto.Message) error {
	b, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO analysis.outbox (topic, key, type, payload) VALUES ($1, $2, $3, $4)`,
		topic, key, string(proto.MessageName(msg)), b)
	return err
}

// nilIfNone stores an empty list as NULL (no flag).
func nilIfNone(xs []string) []string {
	if len(xs) == 0 {
		return nil
	}
	return xs
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// publishAdopted publishes an adopted item's current analysis on
// analysis.result.v1, in the contract's format with the contract's IDs and
// envelope.version, as if it had just been analysed: no AI call. It reports
// false when the item has no current analysis.
func publishAdopted(ctx context.Context, tx pgx.Tx, it *core.Item) (bool, error) {
	var res []byte
	var p core.Provenance
	var route string
	var inputHash []byte
	var version int
	err := tx.QueryRow(ctx, `SELECT result, route, lane, provider, model, prompt_version, taxonomy_version, schema_version,
			input_hash, fallback, analyzed_at, version
		FROM analysis.analyses WHERE created_at = $1 AND content_id = $2 AND is_current`, it.CreatedAt, it.ContentID).
		Scan(&res, &route, &p.Lane, &p.Provider, &p.Model, &p.PromptVersion, &p.TaxonomyVersion, &p.SchemaVersion,
			&inputHash, &p.Fallback, &p.AnalyzedAt, &version)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var x core.Result
	if err := json.Unmarshal(res, &x); err != nil {
		return false, fmt.Errorf("adopted %s: stored result: %w", it.ContentID, err)
	}
	ev := ItemEvent(it, &x, &p, route, inputHash, version)
	if ev.AuthorBot, err = authorBot(ctx, tx, it.Platform, it.AuthorKey); err != nil {
		return false, err
	}
	return true, outbox(ctx, tx, topics.AnalysisResult, it.ContentID, ev)
}
