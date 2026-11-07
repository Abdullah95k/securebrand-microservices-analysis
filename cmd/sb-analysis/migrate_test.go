package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/app"
	"github.com/abdullah95k/securebrand-microservices-analysis/internal/testenv"
)

// The migrate hook needs only Postgres: Kafka and Valkey certificates that
// can't be read don't stop it (they stop every command that builds the
// app).
func TestMigrateNeedsOnlyPostgres(t *testing.T) {
	db := testenv.Postgres(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	missing := filepath.Join(dir, "missing-ca.pem")
	if err := os.WriteFile(cfg, []byte(fmt.Sprintf(`postgres: {dsn: %q}
kafka: {tls: {enabled: true, ca_file: %q}}
valkey: {addr: "127.0.0.1:1", tls: {enabled: true, ca_file: %q}}
`, db.Config().ConnString(), missing, missing)), 0o600); err != nil {
		t.Fatal(err)
	}
	// The app's own setup fails on the Kafka certificate...
	err := withApp("check", []string{"--config", cfg}, nil, func(context.Context, *app.App) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "kafka") {
		t.Fatalf("app setup with an unreadable Kafka CA: %v", err)
	}
	// ...the migrations don't need it.
	if err := migrateCmd([]string{"--config", cfg}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	var n int
	if err := db.QueryRow(context.Background(), `SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'analysis' AND table_name = 'content_items'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("migrations not applied: %d %v", n, err)
	}
	// Again: nothing to do.
	if err := migrateCmd([]string{"--config", cfg}); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}
