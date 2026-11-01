package store

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	analysisv1 "github.com/abdullah95k/securebrand-microservices/gen/go/securebrand/analysis/v1"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/adapter"
	"github.com/abdullah95k/securebrand-microservices/internal/analysis/core"
	"github.com/abdullah95k/securebrand-microservices/internal/topics"
)

// lockAuthors keeps bot scoring to one run at a time.
const lockAuthors = 4200104

// BotScorer turns an author's signals into a score, a level and reasons.
type BotScorer func(core.AuthorSignals) (score int, level string, reasons []string)

// BotRun summarises a scoring run.
type BotRun struct {
	Scored, Flagged, Changed int
}

// authorSignals computes the behaviour of every author with at least
// minItems items since since, and of smaller accounts that took part in
// coordination (pages replying on their own page excluded).
// Formulaic texts (contest entries, prayers, tags) never count as
// repetition or coordination: thousands of people write the same "تم".
const authorSignals = `
WITH w AS MATERIALIZED (
  SELECT c.platform, c.author_key, c.created_at, c.content_id, c.kind, c.source_id,
         coalesce(c.thread_id, c.content_id) AS thread,
         md5(lower(regexp_replace(btrim(c.text), '\s+', ' ', 'g'))) AS th,
         (c.created_at AT TIME ZONE $3)::date AS day,
         char_length(c.text) AS len,
         coalesce(array_length(regexp_split_to_array(btrim(c.text), '\s+'), 1), 0) AS words,
         coalesce(a.intent IN ('giveaway_entry', 'greeting_religious', 'tagging'), false) AS formulaic,
         coalesce(a.spam OR a.intent = 'spam_ad' OR a.ad_type IS NOT NULL OR 'fraud_scam' = ANY(coalesce(a.legal, '{}')), false) AS spammy
  FROM analysis.content_items c
  LEFT JOIN analysis.analyses a ON a.created_at = c.created_at AND a.content_id = c.content_id AND a.is_current
  WHERE c.created_at >= $1 AND c.author_key IS NOT NULL AND NOT c.author_is_source AND c.text IS NOT NULL AND c.status <> 'redacted'
),
-- Coordination: the same substantial text from at least $4 authors on
-- the same (Iraq) day. Long quotes repeated over weeks do not count.
ct AS (
  SELECT platform, th, day FROM w WHERE len >= 30 AND words >= 5 AND NOT formulaic
  GROUP BY 1, 2, 3 HAVING count(DISTINCT author_key) >= $4),
wc AS MATERIALIZED (
  SELECT w.*, (ct.th IS NOT NULL) AS coord FROM w LEFT JOIN ct USING (platform, th, day)),
-- Scored: authors with at least $2 items, and low-volume accounts (2+
-- items) that took part in coordination (networks of accounts that each
-- post a little).
act AS (
  SELECT platform, author_key FROM wc GROUP BY 1, 2
  HAVING count(*) >= $2 OR (count(*) >= 2 AND bool_or(coord))),
aw AS MATERIALIZED (SELECT wc.* FROM wc JOIN act USING (platform, author_key)),
base AS (
  SELECT platform, author_key, count(*) AS items,
         count(DISTINCT day) AS active_days,
         count(DISTINCT source_id) AS sources,
         count(DISTINCT extract(hour FROM created_at AT TIME ZONE $3)) AS active_hours,
         count(*) FILTER (WHERE spammy) AS spammy,
         count(*) FILTER (WHERE coord) AS coordinated,
         min(created_at) AS first_seen, max(created_at) AS last_seen
  FROM aw GROUP BY 1, 2),
hourly AS (
  SELECT platform, author_key, max(n) AS max_per_hour FROM (
    SELECT platform, author_key, date_trunc('hour', created_at) AS h, count(*) AS n FROM aw GROUP BY 1, 2, 3) x
  GROUP BY 1, 2),
rep AS (
  SELECT platform, author_key, sum(n) AS repeated FROM (
    SELECT platform, author_key, th, count(*) AS n, count(DISTINCT thread) AS threads
    FROM aw WHERE len >= 15 AND NOT formulaic GROUP BY 1, 2, 3) x
  WHERE threads >= 3 GROUP BY 1, 2),
fast AS (
  SELECT aw.platform, aw.author_key, count(*) AS base,
         count(*) FILTER (WHERE aw.created_at - ci.created_at BETWEEN interval '0' AND interval '60 seconds') AS fast
  FROM aw JOIN analysis.content_index ci ON ci.content_id = aw.thread
  WHERE aw.kind <> 'post' AND aw.thread <> aw.content_id GROUP BY 1, 2)
SELECT b.platform, b.author_key, b.items, b.active_days, coalesce(h.max_per_hour, 0), b.sources, b.active_hours,
       coalesce(r.repeated, 0), b.coordinated, coalesce(f.fast, 0), coalesce(f.base, 0), b.spammy,
       b.first_seen, b.last_seen
FROM base b
LEFT JOIN hourly h USING (platform, author_key)
LEFT JOIN rep r USING (platform, author_key)
LEFT JOIN fast f USING (platform, author_key)`

// AuthorSignals returns the behaviour of the authors active since since.
func (s *Store) AuthorSignals(ctx context.Context, since time.Time, minItems, coordAuthors int) ([]core.AuthorSignals, error) {
	rows, err := s.DB.Query(ctx, authorSignals, since, minItems, s.Loc.String(), coordAuthors)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.AuthorSignals
	for rows.Next() {
		var a core.AuthorSignals
		var repeated, coordinated, fast, spammy int
		if err := rows.Scan(&a.Platform, &a.AuthorKey, &a.Items, &a.ActiveDays, &a.MaxPerHour, &a.Sources, &a.ActiveHours,
			&repeated, &coordinated, &fast, &a.FastBase, &spammy, &a.FirstSeen, &a.LastSeen); err != nil {
			return nil, err
		}
		n := float64(max(a.Items, 1))
		a.Repeated, a.Coordinated, a.Spammy = float64(repeated)/n, float64(coordinated)/n, float64(spammy)/n
		a.CoordinatedItems = coordinated
		if a.FastBase > 0 {
			a.Fast = float64(fast) / float64(a.FastBase)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ScoreAuthors scores every author active in the window and stores the
// result. When an author's bot level changes, the days of their items are
// rebuilt (rollups count bot items) and an AuthorFlag is published.
// Authors not active in the window keep their last level. An owner's
// override always wins over the score.
func (s *Store) ScoreAuthors(ctx context.Context, window time.Duration, minItems, coordAuthors int, score BotScorer) (BotRun, error) {
	var run BotRun
	sigs, err := s.AuthorSignals(ctx, time.Now().Add(-window), minItems, coordAuthors)
	if err != nil {
		return run, err
	}
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, lockAuthors).Scan(&locked); err != nil {
			return err
		}
		if !locked {
			return nil // another replica is scoring
		}
		for _, a := range sigs {
			sc, level, reasons := score(a)
			if reasons == nil {
				reasons = []string{}
			}
			var before, after *string
			err := tx.QueryRow(ctx, `SELECT bot FROM analysis.authors WHERE platform = $1 AND author_key = $2`, a.Platform, a.AuthorKey).Scan(&before)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO analysis.authors (platform, author_key, items, active_days, per_day, max_per_hour, sources,
				active_hours, repeated, coordinated, fast, spammy, score, level, reasons, first_seen, last_seen, scored_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,now())
				ON CONFLICT (platform, author_key) DO UPDATE SET items = EXCLUDED.items, active_days = EXCLUDED.active_days,
					per_day = EXCLUDED.per_day, max_per_hour = EXCLUDED.max_per_hour, sources = EXCLUDED.sources,
					active_hours = EXCLUDED.active_hours, repeated = EXCLUDED.repeated, coordinated = EXCLUDED.coordinated,
					fast = EXCLUDED.fast, spammy = EXCLUDED.spammy, score = EXCLUDED.score, level = EXCLUDED.level,
					reasons = EXCLUDED.reasons, first_seen = LEAST(analysis.authors.first_seen, EXCLUDED.first_seen),
					last_seen = EXCLUDED.last_seen, scored_at = now()
				RETURNING bot`,
				a.Platform, a.AuthorKey, a.Items, a.ActiveDays, a.PerDay(), a.MaxPerHour, a.Sources, a.ActiveHours, a.Repeated,
				a.Coordinated, a.Fast, a.Spammy, sc, level, reasons, a.FirstSeen, a.LastSeen).Scan(&after); err != nil {
				return err
			}
			run.Scored++
			if after != nil {
				run.Flagged++
			}
			if deref(before) != deref(after) {
				run.Changed++
				if err := s.authorChanged(ctx, tx, a.Platform, a.AuthorKey, deref(after), sc, reasons); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return run, err
}

// OverrideAuthor records the owner's decision on an author: "bot",
// "human", or "" (back to the score).
func (s *Store) OverrideAuthor(ctx context.Context, platform string, key []byte, override string) error {
	if override != "" && override != "bot" && override != "human" {
		return fmt.Errorf("override %q: want bot, human or none", override)
	}
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		var before *string
		err := tx.QueryRow(ctx, `SELECT bot FROM analysis.authors WHERE platform = $1 AND author_key = $2 FOR UPDATE`, platform, key).Scan(&before)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var after *string
		var score int
		var reasons []string
		if err := tx.QueryRow(ctx, `INSERT INTO analysis.authors (platform, author_key, override) VALUES ($1, $2, NULLIF($3, ''))
			ON CONFLICT (platform, author_key) DO UPDATE SET override = EXCLUDED.override
			RETURNING bot, score, reasons`, platform, key, override).Scan(&after, &score, &reasons); err != nil {
			return err
		}
		if deref(before) == deref(after) {
			return nil
		}
		return s.authorChanged(ctx, tx, platform, key, deref(after), score, reasons)
	})
}

// authorChanged rebuilds the days of the author's items and publishes the
// new level.
func (s *Store) authorChanged(ctx context.Context, tx pgx.Tx, platform string, key []byte, bot string, score int, reasons []string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO analysis.rollup_dirty (day)
		SELECT DISTINCT (created_at AT TIME ZONE $3)::date FROM analysis.analyses WHERE platform = $1 AND author_key = $2 AND is_current
		ON CONFLICT (day) DO UPDATE SET marked_at = now()`, platform, key, s.Loc.String()); err != nil {
		return err
	}
	now := time.Now()
	k := hex.EncodeToString(key)
	return outbox(ctx, tx, topics.AnalysisAuthor, k, &analysisv1.AuthorFlag{
		EventId: fmt.Sprintf("%s:%d", k, now.UnixMilli()), Platform: adapter.PlatformEnum(platform), AuthorKey: key,
		Bot: bot, Score: int32(score), Reasons: reasons, ScoredAt: timestamppb.New(now)})
}

// AuthorBot returns an author's bot level in effect ("" when not flagged).
func authorBot(ctx context.Context, tx pgx.Tx, platform string, key []byte) (string, error) {
	if len(key) == 0 {
		return "", nil
	}
	var bot *string
	err := tx.QueryRow(ctx, `SELECT bot FROM analysis.authors WHERE platform = $1 AND author_key = $2`, platform, key).Scan(&bot)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return deref(bot), err
}

// AuthorRow is an author as the owner reviews it (the key is the
// pseudonymous HMAC, never a platform id).
type AuthorRow struct {
	Platform string
	Key      []byte
	Items    int
	PerDay   float64
	Score    int
	Bot      string
	Override string
	Reasons  []string
	ScoredAt *time.Time
	LastSeen *time.Time
}

// Authors lists flagged authors (or all scored ones), highest score first.
func (s *Store) Authors(ctx context.Context, flaggedOnly bool, limit int) ([]AuthorRow, error) {
	rows, err := s.DB.Query(ctx, `SELECT platform, author_key, items, per_day, score, coalesce(bot, ''), coalesce(override, ''), reasons,
		scored_at, last_seen FROM analysis.authors WHERE NOT $1 OR bot IS NOT NULL ORDER BY score DESC, items DESC LIMIT $2`, flaggedOnly, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuthorRow
	for rows.Next() {
		var a AuthorRow
		if err := rows.Scan(&a.Platform, &a.Key, &a.Items, &a.PerDay, &a.Score, &a.Bot, &a.Override, &a.Reasons, &a.ScoredAt, &a.LastSeen); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
