package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/config"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/testenv"
)

// testStore migrates a fresh database.
func testStore(t *testing.T) *Store {
	t.Helper()
	db := testenv.Postgres(t)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Asia/Baghdad")
	s := New(db, loc)
	s.ErasureMode = config.ErasureApply // the erasure tests check deletion; TestErasureModes sets its own
	return s
}

func TestMigrateUpDownUp(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.EnsureWindow(ctx, time.Now(), 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := MigrateDown(ctx, s.DB); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, s.DB); err != nil {
		t.Fatal(err)
	}
}

// Postgres resolves an unqualified table name in the schema named after
// the user first ("$user", public): with a user called "analysis", an
// unqualified version table was looked up in schema analysis once the first
// migration created it, so a rollback, or a restart, found no versions.
func TestVersionTableIgnoresUserSchema(t *testing.T) {
	db := testenv.Postgres(t)
	ctx := context.Background()
	if _, err := db.Exec(ctx, `DO $$ BEGIN EXECUTE format('CREATE SCHEMA IF NOT EXISTS %I', current_user); END $$`); err != nil {
		t.Fatal(err)
	}
	for i, step := range []func(context.Context, *pgxpool.Pool) error{Migrate, MigrateDown, Migrate, Migrate} {
		if err := step(ctx, db); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	var where []string
	rows, err := db.Query(ctx, `SELECT table_schema FROM information_schema.tables WHERE table_name = 'analysis_goose_db_version'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		where = append(where, s)
	}
	rows.Close()
	if len(where) != 1 || where[0] != "public" {
		t.Fatalf("version tables in schemas %v, want only public", where)
	}
}
