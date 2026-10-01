// Package admin serves operational endpoints on a private port: liveness,
// readiness and pprof. Keeping them off the public listener means probes are
// never load-shed or rate-limited, and profiling data never leaks publicly.
package admin

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/pprof"
	"sync/atomic"
	"time"

	"example.com/skeleton/internal/httpserver"
)

// Server is the admin HTTP server. Bind it to a private interface or port
// that only the orchestrator and operators can reach.
type Server struct {
	http  *httpserver.Server
	ready atomic.Bool
}

// New builds the admin server. WriteTimeout is raised to at least 65s
// because /debug/pprof/profile streams for 30s by default and would
// otherwise be cut off mid-profile.
func New(cfg httpserver.Config, logger *slog.Logger) *Server {
	cfg.WriteTimeout = max(cfg.WriteTimeout, 65*time.Second)
	s := &Server{}
	s.http = httpserver.New(cfg, s.routes(), logger)
	return s
}

// routes uses its own ServeMux. Importing net/http/pprof also registers on
// http.DefaultServeMux as a side effect, so never serve the default mux on a
// public port.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK) // the process is running and serving
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.ready.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	// Index serves every named profile: heap, goroutine, allocs, block,
	// mutex and, on Go 1.27+, goroutineleak.
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	return mux
}

// SetReady flips /readyz. Set it true only after every listener is bound,
// and false as the first step of shutdown so traffic drains away.
func (s *Server) SetReady(ready bool) { s.ready.Store(ready) }

// Addr returns the configured listen address.
func (s *Server) Addr() string { return s.http.Addr() }

// ServeListener serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) ServeListener(ctx context.Context, lis net.Listener) error {
	return s.http.ServeListener(ctx, lis)
}
