// Package httpserver runs a hardened net/http server and the standard
// middleware chain for public REST APIs.
package httpserver

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Config holds every server limit. A zero value means "safe default", never
// "unlimited": http.Server's own zero values disable the timeouts, which lets
// slow or idle clients pin goroutines and file descriptors indefinitely.
type Config struct {
	Addr              string        // default ":8080"
	ReadHeaderTimeout time.Duration // default 5s; the Slowloris guard
	ReadTimeout       time.Duration // default 10s; whole request including body
	WriteTimeout      time.Duration // default 10s; keep above the handler deadline
	IdleTimeout       time.Duration // default 90s; keep above the load balancer's idle timeout
	MaxHeaderBytes    int           // default 64 KiB
	ShutdownTimeout   time.Duration // default 10s; budget for in-flight requests
}

func (c Config) withDefaults() Config {
	c.Addr = cmp.Or(c.Addr, ":8080")
	c.ReadHeaderTimeout = orDefault(c.ReadHeaderTimeout, 5*time.Second)
	c.ReadTimeout = orDefault(c.ReadTimeout, 10*time.Second)
	c.WriteTimeout = orDefault(c.WriteTimeout, 10*time.Second)
	c.IdleTimeout = orDefault(c.IdleTimeout, 90*time.Second)
	c.MaxHeaderBytes = orDefault(c.MaxHeaderBytes, 64<<10)
	c.ShutdownTimeout = orDefault(c.ShutdownTimeout, 10*time.Second)
	return c
}

// orDefault returns v, or def when v is the zero value (or negative).
func orDefault[T int | time.Duration](v, def T) T {
	if v <= 0 {
		return def
	}
	return v
}

// Server wraps http.Server with a context-driven lifecycle.
type Server struct {
	cfg    Config
	srv    *http.Server
	logger *slog.Logger
}

// New builds a server for h. Bind the listener yourself (net.Listen) before
// reporting readiness, then call ServeListener.
func New(cfg Config, h http.Handler, logger *slog.Logger) *Server {
	cfg = cfg.withDefaults()
	return &Server{
		cfg:    cfg,
		logger: logger,
		srv: &http.Server{
			Addr:              cfg.Addr,
			Handler:           h,
			ReadHeaderTimeout: cfg.ReadHeaderTimeout,
			ReadTimeout:       cfg.ReadTimeout,
			WriteTimeout:      cfg.WriteTimeout,
			IdleTimeout:       cfg.IdleTimeout,
			MaxHeaderBytes:    cfg.MaxHeaderBytes,
			// Route net/http's own errors (TLS handshakes, bad requests)
			// into the structured log instead of stderr.
			ErrorLog: slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		},
	}
}

// Addr returns the configured listen address.
func (s *Server) Addr() string { return s.cfg.Addr }

// ServeListener serves until ctx is cancelled, then shuts down gracefully:
// stop accepting, let in-flight requests finish within ShutdownTimeout, and
// force-close what remains. It returns nil after a clean shutdown.
//
// Shutdown does not wait for hijacked connections (WebSockets); their owners
// must watch ctx themselves.
func (s *Server) ServeListener(ctx context.Context, lis net.Listener) error {
	errCh := make(chan error, 1)
	go func() { errCh <- s.srv.Serve(lis) }()

	select {
	case err := <-errCh: // Serve failed before shutdown was requested
		_ = s.srv.Close() // drop connections that were already accepted
		return fmt.Errorf("http serve %s: %w", lis.Addr(), err)
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.ShutdownTimeout)
	defer cancel()
	if err := s.srv.Shutdown(shutdownCtx); err != nil {
		s.logger.WarnContext(ctx, "http graceful shutdown did not finish, closing connections",
			"addr", lis.Addr().String(), "timeout", s.cfg.ShutdownTimeout, "err", err)
		if cerr := s.srv.Close(); cerr != nil {
			return fmt.Errorf("http close: %w", cerr)
		}
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http serve: %w", err)
	}
	return nil
}
