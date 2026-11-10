package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A record Kafka refuses (its topic not writable) waits, with its error
// and a backoff, while the records around it go out; once due again it is
// tried again, and published when it can be. Nothing is dropped.
func TestOutboxRecordThatFailsWaitsAndTheRestFlow(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for _, topic := range []string{"analysis.result.v1", "content.media.processed.v1", "analysis.media.v1"} {
		if _, err := s.DB.Exec(ctx, `INSERT INTO analysis.outbox (topic, key, type, payload) VALUES ($1, 'k', 't', '\x01')`, topic); err != nil {
			t.Fatal(err)
		}
	}
	refused := errors.New("TOPIC_AUTHORIZATION_FAILED")
	refuse := func(_ context.Context, recs []OutboxRecord) []error {
		errs := make([]error, len(recs))
		for i, r := range recs {
			if r.Topic == "content.media.processed.v1" {
				errs[i] = refused
			}
		}
		return errs
	}
	pub, failed, err := s.DrainOutbox(ctx, 10, refuse)
	if err != nil || pub != 2 || failed != 1 {
		t.Fatalf("published %d, failed %d, %v: want 2 and 1", pub, failed, err)
	}
	var topic, last string
	var attempts int
	var later bool
	if err := s.DB.QueryRow(ctx, `SELECT topic, attempts, last_error, not_before > now() FROM analysis.outbox`).Scan(&topic, &attempts, &last, &later); err != nil {
		t.Fatal(err)
	}
	if topic != "content.media.processed.v1" || attempts != 1 || !strings.Contains(last, "TOPIC_AUTHORIZATION_FAILED") || !later {
		t.Fatalf("held record: %s, %d attempts, %q, later %v", topic, attempts, last, later)
	}
	if held, _ := s.OutboxHeld(ctx); held != 1 {
		t.Fatalf("held %d, want 1", held)
	}

	// Not due yet: the next round doesn't try it, and doesn't block on it.
	if _, err := s.DB.Exec(ctx, `INSERT INTO analysis.outbox (topic, key, type, payload) VALUES ('analysis.author.v1', 'k', 't', '\x01')`); err != nil {
		t.Fatal(err)
	}
	called := 0
	pub, failed, err = s.DrainOutbox(ctx, 10, func(_ context.Context, recs []OutboxRecord) []error {
		called += len(recs)
		return nil // nil: every record went out
	})
	if err != nil || pub != 1 || failed != 0 || called != 1 {
		t.Fatalf("second round: published %d, failed %d, tried %d, %v: want only the new record", pub, failed, called, err)
	}

	// Due again, refused again: the wait grows.
	if _, err := s.DB.Exec(ctx, `UPDATE analysis.outbox SET not_before = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DrainOutbox(ctx, 10, refuse); err != nil {
		t.Fatal(err)
	}
	var wait float64
	if err := s.DB.QueryRow(ctx, `SELECT attempts, extract(epoch FROM not_before - now()) FROM analysis.outbox`).Scan(&attempts, &wait); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || wait < 8 || wait > 11 {
		t.Fatalf("after a second refusal: %d attempts, next try in %.0f s, want 2 and about 10 s", attempts, wait)
	}

	// Allowed at last: published and gone.
	if _, err := s.DB.Exec(ctx, `UPDATE analysis.outbox SET not_before = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if pub, failed, err := s.DrainOutbox(ctx, 10, func(context.Context, []OutboxRecord) []error { return nil }); err != nil || pub != 1 || failed != 0 {
		t.Fatalf("published %d, failed %d, %v", pub, failed, err)
	}
	if n, _ := s.OutboxBacklog(ctx); n != 0 {
		t.Fatalf("%d records left", n)
	}
}
