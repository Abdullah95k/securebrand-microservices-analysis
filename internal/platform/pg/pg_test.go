package pg

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Production's PostgreSQL is behind PgBouncer in transaction mode, which
// keeps no prepared statements: the pool must not rely on them.
func TestConfigFitsATransactionPooler(t *testing.T) {
	c, err := Config("host=db port=5432 user=u dbname=d sslmode=disable", 4)
	if err != nil {
		t.Fatal(err)
	}
	if c.ConnConfig.DefaultQueryExecMode != pgx.QueryExecModeCacheDescribe {
		t.Errorf("exec mode %v, want cache_describe", c.ConnConfig.DefaultQueryExecMode)
	}
	if c.MaxConns != 4 || c.MaxConnIdleTime > 50*time.Second {
		t.Errorf("max conns %d, idle %v", c.MaxConns, c.MaxConnIdleTime)
	}
	// An explicit mode in the DSN wins.
	c, err = Config("postgres://u@db/d?sslmode=disable&default_query_exec_mode=simple_protocol", 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.ConnConfig.DefaultQueryExecMode != pgx.QueryExecModeSimpleProtocol {
		t.Errorf("exec mode %v, want the DSN's simple_protocol", c.ConnConfig.DefaultQueryExecMode)
	}
}

// At start-up a full server (53300) or one starting up (57P03) is waited
// for; any other error, or success, ends the wait.
func TestPingRetriesWhileTheServerIsFull(t *testing.T) {
	full := fmt.Errorf("connect: %w", &pgconn.PgError{Code: "53300", Message: "too many connections"})
	delays := []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	var waited int
	wait := func(context.Context, time.Duration) error { waited++; return nil }
	pings := func(errs ...error) func(context.Context) error {
		return func(context.Context) error {
			e := errs[0]
			if len(errs) > 1 {
				errs = errs[1:]
			}
			return e
		}
	}
	if err := pingRetry(context.Background(), pings(full, full, nil), delays, wait); err != nil || waited != 2 {
		t.Fatalf("full twice, then free: %v after %d waits", err, waited)
	}
	waited = 0
	if err := pingRetry(context.Background(), pings(full), delays, wait); !Transient(err) || waited != 3 {
		t.Fatalf("always full: %v after %d waits, want the 53300 after all 3", err, waited)
	}
	waited = 0
	denied := &pgconn.PgError{Code: "28P01", Message: "password authentication failed"}
	if err := pingRetry(context.Background(), pings(denied), delays, wait); !errors.Is(err, error(denied)) || waited != 0 {
		t.Fatalf("a wrong password is not waited for: %v after %d waits", err, waited)
	}
	if !Transient(&pgconn.PgError{Code: "57P03"}) || Transient(errors.New("dial tcp: i/o timeout")) {
		t.Fatal("transient errors")
	}
}
