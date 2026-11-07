package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestReadinessChecksAtMostOncePerMinute: the kubelet probes every 10
// seconds, but the database is pinged at most once a minute; the answer in
// between, a failure included, is the last one.
func TestReadinessChecksAtMostOncePerMinute(t *testing.T) {
	o := NewOps(":0")
	clock := time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)
	o.now = func() time.Time { return clock }
	pings := 0
	var fail error
	o.AddCheck("postgres", func(context.Context) error { pings++; return fail })
	h := o.Handler()
	probe := func() (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rec.Code, rec.Body.String()
	}

	// Six probes in a minute: one ping.
	for range 6 {
		if code, _ := probe(); code != http.StatusOK {
			t.Fatalf("readyz %d, want 200", code)
		}
		clock = clock.Add(10 * time.Second)
	}
	if pings != 1 {
		t.Fatalf("%d pings in a minute, want 1", pings)
	}

	// A minute on, the next probe pings again; a failure names the check
	// and is kept until the next ping.
	fail = errors.New("too many connections")
	code, body := probe()
	if pings != 2 || code != http.StatusServiceUnavailable || !strings.Contains(body, "postgres: too many connections") {
		t.Fatalf("after a minute: %d pings, %d %q; want 2 pings, 503 naming postgres", pings, code, body)
	}
	fail = nil
	clock = clock.Add(30 * time.Second)
	if code, _ := probe(); code != http.StatusServiceUnavailable || pings != 2 {
		t.Fatalf("30 s after a failure: %d with %d pings, want the kept 503 and no ping", code, pings)
	}
	clock = clock.Add(30 * time.Second)
	if code, _ := probe(); code != http.StatusOK || pings != 3 {
		t.Fatalf("a minute after a failure: %d with %d pings, want 200 from a new ping", code, pings)
	}
}

// TestReadinessCheckOutlivesTheProbe: a probe that gives up early doesn't
// cancel the check, so a slow success isn't kept as a failure.
func TestReadinessCheckOutlivesTheProbe(t *testing.T) {
	o := NewOps(":0")
	o.AddCheck("postgres", func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
			return nil
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	o.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil).WithContext(ctx))
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz %d %q after the probe's deadline, want the check's own 200", rec.Code, rec.Body.String())
	}
}

// TestLivenessNeedsNoDatabase: /healthz runs no check.
func TestLivenessNeedsNoDatabase(t *testing.T) {
	o := NewOps(":0")
	o.AddCheck("postgres", func(context.Context) error { t.Fatal("liveness ran a check"); return nil })
	rec := httptest.NewRecorder()
	o.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz %d, want 200", rec.Code)
	}
}
