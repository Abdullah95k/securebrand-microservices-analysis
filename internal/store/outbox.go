package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

var errNoResult = errors.New("no publish result")

// OutboxRecord is a record waiting to be published.
type OutboxRecord struct {
	ID       int64
	Topic    string
	Key      string
	Type     string
	Payload  []byte
	Attempts int
}

// DrainOutbox locks up to limit records that are due, oldest first, and
// hands them to publish, which returns one error per record (nil when it
// went out), or nil when every record went out. Published records are
// deleted. A record that failed stays,
// with its attempts counted, its error kept and a wait before the next try
// (5 s, doubling, at most 10 minutes), so the records behind it keep
// flowing: one record whose topic is refused no longer holds back
// everything else. Nothing is dropped. It returns how many were published
// and how many failed.
func (s *Store) DrainOutbox(ctx context.Context, limit int, publish func(ctx context.Context, recs []OutboxRecord) []error) (published, failed int, err error) {
	err = pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, topic, key, type, payload, attempts FROM analysis.outbox
			WHERE not_before <= now() ORDER BY not_before, id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
		if err != nil {
			return err
		}
		var recs []OutboxRecord
		for rows.Next() {
			var r OutboxRecord
			if err := rows.Scan(&r.ID, &r.Topic, &r.Key, &r.Type, &r.Payload, &r.Attempts); err != nil {
				rows.Close()
				return err
			}
			recs = append(recs, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(recs) == 0 {
			return err
		}
		errs := publish(ctx, recs)
		var done, held []int64
		var reasons []string
		for i, r := range recs {
			var perr error
			switch {
			case len(errs) == 0:
			case i < len(errs):
				perr = errs[i]
			default:
				// A publisher that didn't answer for every record: keep the
				// unanswered ones for the next round.
				perr = errNoResult
			}
			if perr != nil {
				held = append(held, r.ID)
				reasons = append(reasons, perr.Error())
				continue
			}
			done = append(done, r.ID)
		}
		if len(done) > 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM analysis.outbox WHERE id = ANY($1)`, done); err != nil {
				return err
			}
		}
		if len(held) > 0 {
			if _, err := tx.Exec(ctx, `UPDATE analysis.outbox o SET attempts = o.attempts + 1, last_error = left(f.err, 500),
					not_before = now() + make_interval(secs => least(600, 5 * power(2, o.attempts)))
				FROM unnest($1::bigint[], $2::text[]) AS f(id, err) WHERE o.id = f.id`, held, reasons); err != nil {
				return err
			}
		}
		published, failed = len(done), len(held)
		return nil
	})
	return published, failed, err
}

// OutboxBacklog is the number of records waiting.
func (s *Store) OutboxBacklog(ctx context.Context) (int64, error) {
	var n int64
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM analysis.outbox`).Scan(&n)
	return n, err
}

// OutboxHeld is the number of records waiting after a failed publish.
func (s *Store) OutboxHeld(ctx context.Context) (int64, error) {
	var n int64
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM analysis.outbox WHERE attempts > 0`).Scan(&n)
	return n, err
}
