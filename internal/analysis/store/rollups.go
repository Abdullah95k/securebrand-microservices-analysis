package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// RecomputeDirtyDays rebuilds the daily rollups of up to limit days marked
// dirty, oldest mark first, each from the current analyses and mentions of
// that day (in the store's timezone). A day marked again while it is being
// rebuilt stays dirty for the next round. It returns the days rebuilt.
func (s *Store) RecomputeDirtyDays(ctx context.Context, limit int, settle time.Duration) (int, error) {
	rows, err := s.DB.Query(ctx, `SELECT day, marked_at FROM analysis.rollup_dirty
		WHERE marked_at <= now() - make_interval(secs => $2) ORDER BY marked_at LIMIT $1`, limit, settle.Seconds())
	if err != nil {
		return 0, err
	}
	type dirty struct {
		day    time.Time
		marked time.Time
	}
	var days []dirty
	for rows.Next() {
		var d dirty
		if err := rows.Scan(&d.day, &d.marked); err != nil {
			rows.Close()
			return 0, err
		}
		days = append(days, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, d := range days {
		if err := s.RecomputeDay(ctx, d.day); err != nil {
			return 0, err
		}
		if _, err := s.DB.Exec(ctx, `DELETE FROM analysis.rollup_dirty WHERE day = $1 AND marked_at = $2`, d.day, d.marked); err != nil {
			return 0, err
		}
	}
	return len(days), nil
}

// RecomputeDay rebuilds every rollup of one day.
func (s *Store) RecomputeDay(ctx context.Context, day time.Time) error {
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, s.Loc)
	to := from.AddDate(0, 0, 1)
	d := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	const sentimentCounts = `count(*) FILTER (WHERE sentiment = 'positive'), count(*) FILTER (WHERE sentiment = 'negative'),
		count(*) FILTER (WHERE sentiment = 'neutral'), count(*) FILTER (WHERE sentiment = 'mixed')`
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		for _, q := range []string{
			`DELETE FROM analysis.rollup_entity_daily WHERE day = $1`,
			`DELETE FROM analysis.rollup_entity_topic_daily WHERE day = $1`,
			`DELETE FROM analysis.rollup_topic_daily WHERE day = $1`,
			`DELETE FROM analysis.rollup_source_daily WHERE day = $1`,
		} {
			if _, err := tx.Exec(ctx, q, d); err != nil {
				return err
			}
		}
		for _, q := range []string{
			`INSERT INTO analysis.rollup_entity_daily (day, entity_id, platform, source_id, sentiment, mentions, score_sum)
			 SELECT $1::date, entity_id, platform, source_id, sentiment, count(*), coalesce(sum(score), 0)
			 FROM analysis.entity_mentions WHERE created_at >= $2 AND created_at < $3 AND entity_id IS NOT NULL
			 GROUP BY entity_id, platform, source_id, sentiment`,
			`INSERT INTO analysis.rollup_entity_topic_daily (day, entity_id, platform, topic, mentions, positive, negative, neutral, mixed)
			 SELECT $1::date, entity_id, platform, t.topic, count(*), ` + sentimentCounts + `
			 FROM analysis.entity_mentions m CROSS JOIN LATERAL unnest(coalesce(m.topics, '{}'::text[])) AS t(topic)
			 WHERE m.created_at >= $2 AND m.created_at < $3 AND m.entity_id IS NOT NULL
			 GROUP BY entity_id, platform, t.topic`,
			`INSERT INTO analysis.rollup_topic_daily (day, platform, source_id, topic, items, positive, negative, neutral, mixed)
			 SELECT $1::date, platform, source_id, t.topic, count(*), ` + sentimentCounts + `
			 FROM analysis.analyses a CROSS JOIN LATERAL unnest(coalesce(a.topics, '{}'::text[])) AS t(topic)
			 WHERE a.is_current AND a.created_at >= $2 AND a.created_at < $3
			 GROUP BY platform, source_id, t.topic`,
			`INSERT INTO analysis.rollup_source_daily (day, platform, source_id, kind, items, positive, negative, neutral, mixed,
			   complaints, urgent, score_sum)
			 SELECT $1::date, platform, source_id, kind, count(*), ` + sentimentCounts + `,
			   count(*) FILTER (WHERE intent = 'complaint'), count(*) FILTER (WHERE urgent), coalesce(sum(sentiment_score), 0)
			 FROM analysis.analyses WHERE is_current AND created_at >= $2 AND created_at < $3
			 GROUP BY platform, source_id, kind`,
		} {
			if _, err := tx.Exec(ctx, q, d, from, to); err != nil {
				return err
			}
		}
		return nil
	})
}
