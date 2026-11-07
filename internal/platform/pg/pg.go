// Package pg opens pgx connection pools.
package pg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// Open creates a pool and verifies connectivity. A server that is full or
// starting up (several services starting at once share 50 connections)
// is retried for about two minutes before Open gives up.
func Open(ctx context.Context, dsn string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := Config(dsn, maxConns)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pg: connect: %w", err)
	}
	if err := pingRetry(ctx, pool.Ping, startupDelays, sleep); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: ping: %w", err)
	}
	return pool, nil
}

// startupDelays are the waits between attempts while the server is full
// or starting up: about two minutes in all.
var startupDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
	15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// pingRetry pings, waiting and trying again while the error is one that
// passes: too many connections (53300) or the server starting up (57P03).
func pingRetry(ctx context.Context, ping func(context.Context) error, delays []time.Duration,
	wait func(context.Context, time.Duration) error) error {
	err := ping(ctx)
	for _, d := range delays {
		if err == nil || !Transient(err) {
			return err
		}
		slog.Warn("pg: server busy at start; retrying", "in", d, "err", err)
		if werr := wait(ctx, d); werr != nil {
			return err
		}
		err = ping(ctx)
	}
	return err
}

// Transient reports whether err says the server can't take the connection
// now but will soon: too_many_connections (53300) or cannot_connect_now
// (57P03).
func Transient(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "53300" || pgErr.Code == "57P03"
	}
	return false
}
