// Package pg opens pgx connection pools.
package pg

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config parses dsn into a pool config that works through a connection
// pooler in transaction mode (production's PgBouncer, which keeps no
// prepared statements), as well as against PostgreSQL directly:
//   - statements are described once and their description cached, but never
//     kept prepared on a server session (pgx's default prepares and caches
//     statements on the session, and a pooler hands the next transaction to
//     another session): QueryExecModeCacheDescribe, unless the DSN sets
//     default_query_exec_mode itself;
//   - idle connections close after 30 seconds, under the platform's
//     idle_session_timeout (60 seconds);
//   - at most maxConns connections (when above 0).
func Config(dsn string, maxConns int32) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pg: parse dsn: %w", err)
	}
	if !strings.Contains(dsn, "default_query_exec_mode") {
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheDescribe
	}
	cfg.MaxConnIdleTime = 30 * time.Second
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	return cfg, nil
}

// Open creates a pool and verifies connectivity.
func Open(ctx context.Context, dsn string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := Config(dsn, maxConns)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pg: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: ping: %w", err)
	}
	return pool, nil
}
