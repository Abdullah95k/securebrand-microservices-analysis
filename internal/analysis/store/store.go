// Package store is the analysis service's Postgres layer (schema
// "analysis"): content and its analyses, the work queue, provider requests,
// the outbox, media jobs, the entity registry, rollups, erasure and the
// agent's read-only access.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	analysismigrations "github.com/abdullah95k/securebrand-microservices/migrations/analysis"
)

// Store is the analysis database.
type Store struct {
	DB *pgxpool.Pool
	// Loc is the timezone of rollup and budget days (Asia/Baghdad).
	Loc *time.Location

	mu     sync.Mutex
	months map[string]bool // partitions known to exist
}

// New wraps a pool.
func New(db *pgxpool.Pool, loc *time.Location) *Store {
	if loc == nil {
		loc = time.UTC
	}
	return &Store{DB: db, Loc: loc, months: map[string]bool{}}
}

// VersionTable is goose's version table for the analysis migrations, apart
// from fbs's own.
const VersionTable = "analysis_goose_db_version"

func provider(pool *pgxpool.Pool) (*goose.Provider, *sql.DB, error) {
	db := stdlib.OpenDBFromPool(pool)
	p, err := goose.NewProvider(goose.DialectPostgres, db, analysismigrations.FS, goose.WithTableName(VersionTable))
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("analysis store: goose: %w", err)
	}
	return p, db, nil
}

// Migrate applies the analysis migrations.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	p, db, err := provider(pool)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("analysis store: migrate: %w", err)
	}
	return nil
}

// MigrateDown rolls back one analysis migration.
func MigrateDown(ctx context.Context, pool *pgxpool.Pool) error {
	p, db, err := provider(pool)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	_, err = p.Down(ctx)
	return err
}

// monthlyTables share the item's creation month as partition key.
var monthlyTables = []string{"content_items", "analyses", "entity_mentions"}

const partitionLock = 4_200_101

// EnsureMonths creates the monthly partitions holding ts for every
// partitioned table, once per process per month.
func (s *Store) EnsureMonths(ctx context.Context, ts ...time.Time) error {
	// Creation is rare: hold the lock while creating, so no caller inserts
	// into a month another caller is still creating.
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range ts {
		m := monthStart(t)
		k := m.Format("2006-01")
		if s.months[k] {
			continue
		}
		if err := s.createMonth(ctx, m); err != nil {
			return err
		}
		s.months[k] = true
	}
	return nil
}

// EnsureWindow creates partitions from monthsBack before now to monthsAhead.
func (s *Store) EnsureWindow(ctx context.Context, now time.Time, monthsBack, monthsAhead int) error {
	var ts []time.Time
	start := monthStart(now).AddDate(0, -monthsBack, 0)
	for m := start; !m.After(monthStart(now).AddDate(0, monthsAhead, 0)); m = m.AddDate(0, 1, 0) {
		ts = append(ts, m)
	}
	return s.EnsureMonths(ctx, ts...)
}

func (s *Store) createMonth(ctx context.Context, m time.Time) error {
	return pgx.BeginFunc(ctx, s.DB, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", partitionLock); err != nil {
			return err
		}
		for _, t := range monthlyTables {
			name := fmt.Sprintf("%s_y%04dm%02d", t, m.Year(), int(m.Month()))
			var exists bool
			if err := tx.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", "analysis."+name).Scan(&exists); err != nil {
				return err
			}
			if exists {
				continue
			}
			ddl := fmt.Sprintf("CREATE TABLE analysis.%s PARTITION OF analysis.%s FOR VALUES FROM ('%s') TO ('%s')",
				pgx.Identifier{name}.Sanitize(), pgx.Identifier{t}.Sanitize(),
				m.Format(time.RFC3339), m.AddDate(0, 1, 0).Format(time.RFC3339))
			if _, err := tx.Exec(ctx, ddl); err != nil {
				return fmt.Errorf("analysis store: partition %s: %w", name, err)
			}
		}
		return nil
	})
}

func monthStart(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// Day is the rollup day (in the store's timezone) of t.
func (s *Store) Day(t time.Time) time.Time {
	l := t.In(s.Loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}

// DayStart is the instant a day (in the store's timezone) starts.
func (s *Store) DayStart(t time.Time) time.Time {
	l := t.In(s.Loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, s.Loc)
}
