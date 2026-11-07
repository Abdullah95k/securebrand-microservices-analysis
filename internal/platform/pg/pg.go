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
	return open(ctx, dsn, maxConns, startupDelays)
}

// OpenPatient is Open waiting up to about wait for a free connection: for
// the migrate hook, which blocks a deployment when it fails, so it would
// rather wait out a full database than give up after two minutes.
func OpenPatient(ctx context.Context, dsn string, maxConns int32, wait time.Duration) (*pgxpool.Pool, error) {
	return open(ctx, dsn, maxConns, Patience(wait))
}

func open(ctx context.Context, dsn string, maxConns int32, delays []time.Duration) (*pgxpool.Pool, error) {
	cfg, err := Config(dsn, maxConns)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pg: connect: %w", err)
	}
	if err := retry(ctx, "connect", pool.Ping, delays, sleep); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: ping: %w", err)
	}
	return pool, nil
}

// Retry runs fn, waiting and running it again while it fails with a
// Transient error, for up to about wait in all. Any other error, or
// success, ends it at once.
func Retry(ctx context.Context, step string, wait time.Duration, fn func(context.Context) error) error {
	return retry(ctx, step, fn, Patience(wait), sleep)
}

// startupDelays are the waits between attempts while the server is full
// or starting up: about two minutes in all.
var startupDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
	15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second, 15 * time.Second}

// Patience returns waits that add up to at least total: 1, 2, 4, 8, 16
// seconds, then 30 seconds each.
func Patience(total time.Duration) []time.Duration {
	var out []time.Duration
	d, sum := time.Second, time.Duration(0)
	for sum < total {
		out = append(out, d)
		sum += d
		d = min(2*d, 30*time.Second)
	}
	return out
}

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

// retry runs fn, waiting and trying again while the error is one that
// passes (Transient), once per delay.
func retry(ctx context.Context, step string, fn func(context.Context) error, delays []time.Duration,
	wait func(context.Context, time.Duration) error) error {
	err := fn(ctx)
	for _, d := range delays {
		if err == nil || !Transient(err) {
			return err
		}
		slog.Warn("pg: server busy; retrying", "step", step, "in", d, "err", err)
		if werr := wait(ctx, d); werr != nil {
			return err
		}
		err = fn(ctx)
	}
	return err
}

// Transient reports whether err says the database can't take the
// connection or the query now but will soon:
//   - PostgreSQL's too_many_connections (53300), passed on by PgBouncer
//     when it can't open a server connection, or cannot_connect_now
//     (57P03);
//   - PgBouncer's own errors while its pool is full or its logins to the
//     server keep failing (sent as 08P01 with these messages):
//     query_wait_timeout, "server login has been failing ...
//     (server_login_retry)", "no more connections allowed",
//     client_login_timeout, "pgbouncer cannot connect to server".
func Transient(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch {
	case pgErr.Code == "53300" || pgErr.Code == "57P03":
		return true
	case strings.HasPrefix(pgErr.Code, "08"):
		msg := strings.ToLower(pgErr.Message)
		for _, s := range poolerBusy {
			if strings.Contains(msg, s) {
				return true
			}
		}
	}
	return false
}

// poolerBusy are PgBouncer's messages for a full pool or a server that
// can't take a login yet.
var poolerBusy = []string{
	"query_wait_timeout",
	"server_login_retry",
	"server login has been failing",
	"no more connections allowed",
	"client_login_timeout",
	"pgbouncer cannot connect to server",
}
