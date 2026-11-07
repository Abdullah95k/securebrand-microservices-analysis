package pg

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
