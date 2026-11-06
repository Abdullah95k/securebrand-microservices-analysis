package store

import (
	"context"
	"testing"
	"time"

	"github.com/abdullah95k/securebrand-microservices/internal/config"
	"github.com/abdullah95k/securebrand-microservices/internal/testenv"
)

// testStore migrates a fresh database (fbs and analysis migrations).
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
