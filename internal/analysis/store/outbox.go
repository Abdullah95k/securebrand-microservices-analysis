package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// OutboxRecord is a record waiting to be published.
type OutboxRecord struct {
	ID      int64
	Topic   string
	Key     string
	Type    string
	Payload []byte
}

// DrainOutbox locks up to limit outbox records, hands them to publish and
// deletes them when publish succeeds; otherwise they stay for the next
// round. It returns how many were published.
func (s *Store) DrainOutbox(ctx context.Context, limit int, publish func(ctx context.Context, recs []OutboxRecord) error) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, topic, key, type, payload FROM analysis.outbox ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		var recs []OutboxRecord
		for rows.Next() {
			var r OutboxRecord
			if err := rows.Scan(&r.ID, &r.Topic, &r.Key, &r.Type, &r.Payload); err != nil {
				rows.Close()
				return err
			}
			recs = append(recs, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(recs) == 0 {
			return err
		}
		if err := publish(ctx, recs); err != nil {
			return err
		}
		ids := make([]int64, len(recs))
		for i, r := range recs {
			ids[i] = r.ID
		}
		if _, err := tx.Exec(ctx, `DELETE FROM analysis.outbox WHERE id = ANY($1)`, ids); err != nil {
			return err
		}
		n = len(recs)
		return nil
	})
	return n, err
}

// OutboxBacklog is the number of records waiting.
func (s *Store) OutboxBacklog(ctx context.Context) (int64, error) {
	var n int64
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM analysis.outbox`).Scan(&n)
	return n, err
}
