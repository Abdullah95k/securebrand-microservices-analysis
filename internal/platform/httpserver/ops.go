// Package httpserver provides the operations server every binary exposes:
// /metrics, /healthz (liveness) and /readyz (readiness checks).
package httpserver

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/abdullah95k/securebrand-microservices-analysis/internal/platform/metrics"
)

// Check reports whether a dependency is usable.
type Check func(ctx context.Context) error

// Ops is the operations HTTP server.
type Ops struct {
	addr   string
	mu     sync.RWMutex
	checks map[string]Check

	// The readiness checks run at most once per CheckEvery; probes in
	// between get the last answer. The kubelet probes every 10 seconds, and
	// a database ping each time kept a server connection of the shared
	// database busy.
	CheckEvery time.Duration
	now        func() time.Time
	cache      sync.Mutex
	checked    time.Time
	result     error
}

// DefaultCheckEvery is how often the readiness checks run.
const DefaultCheckEvery = time.Minute

func NewOps(addr string) *Ops {
	return &Ops{addr: addr, checks: map[string]Check{}, CheckEvery: DefaultCheckEvery, now: time.Now}
}

// AddCheck registers a readiness check.
func (o *Ops) AddCheck(name string, c Check) {
	o.mu.Lock()
	o.checks[name] = c
	o.mu.Unlock()
}

func (o *Ops) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := o.ready(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

// ready runs the checks, or returns their last result while it is fresher
// than CheckEvery. A failure names the check, and is kept as long as a
// success: a pod not ready for a minute is better than a database pinged
// every 10 seconds while it is full.
func (o *Ops) ready(ctx context.Context) error {
	o.cache.Lock()
	defer o.cache.Unlock()
	now := o.now()
	if !o.checked.IsZero() && now.Sub(o.checked) < o.CheckEvery {
		return o.result
	}
	// Not the probe's own deadline: the answer is kept for a minute, so a
	// probe that gave up early mustn't turn a slow success into a failure.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	o.mu.RLock()
	var err error
	for name, c := range o.checks {
		if cerr := c(ctx); cerr != nil {
			err = errors.New(name + ": " + cerr.Error())
			break
		}
	}
	o.mu.RUnlock()
	o.checked, o.result = now, err
	return err
}

// Run serves until ctx is done.
func (o *Ops) Run(ctx context.Context) error {
	return Serve(ctx, o.addr, o.Handler())
}

// Option configures the server Serve runs.
type Option func(*http.Server)

// WithReadTimeout bounds reading a whole request, body included.
func WithReadTimeout(d time.Duration) Option {
	return func(s *http.Server) {
		if d > 0 {
			s.ReadTimeout = d
		}
	}
}

// Serve runs an HTTP server and shuts it down gracefully when ctx is done.
// Idle keep-alive connections are closed after 2 minutes, so clients that
// hold connections open (authenticated or not) can't exhaust descriptors.
func Serve(ctx context.Context, addr string, h http.Handler, opts ...Option) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	for _, o := range opts {
		o(srv)
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("http listening", "addr", addr)
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
