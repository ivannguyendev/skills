// Package grpcserver builds a production gRPC server: tracing, interceptors,
// keepalive, health checking and an ordered graceful shutdown.
package grpcserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc/filters"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"

	"example.com/skeleton/internal/resilience"
)

// Config tunes the server. Zero values get safe defaults in New.
type Config struct {
	Addr              string        // listen address, default ":50051"
	ShutdownTimeout   time.Duration // budget for in-flight RPCs, default 15s
	DrainDelay        time.Duration // pause after NOT_SERVING so LBs stop routing here
	EnableReflection  bool          // dev only: exposes the full schema to any caller
	TraceHealthChecks bool          // probes every few seconds would flood the trace backend
	// Limiter, when set, sheds unary RPCs under overload. Share one instance
	// with the HTTP server so both transports draw on a single budget.
	Limiter       *resilience.Limiter
	ServerOptions []grpc.ServerOption
}

// Server owns the grpc.Server and its health service. It implements
// grpc.ServiceRegistrar, so generated code registers on it directly:
//
//	srv := grpcserver.New(cfg, logger)
//	ordersv1.RegisterOrderServiceServer(srv, orders.NewServer(repo, srv.Draining()))
type Server struct {
	cfg      Config
	grpc     *grpc.Server
	health   *health.Server
	draining chan struct{}
	logger   *slog.Logger
}

// New builds the server. Register services before calling Serve.
func New(cfg Config, logger *slog.Logger) *Server {
	if cfg.Addr == "" {
		cfg.Addr = ":50051"
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 15 * time.Second
	}

	var traceOpts []otelgrpc.Option
	if !cfg.TraceHealthChecks {
		traceOpts = append(traceOpts, otelgrpc.WithFilter(filters.Not(filters.HealthCheck())))
	}

	// Load shedding sits right after recovery: rejecting early is cheap, and
	// shed RPCs are still counted by the stats handler without a log line each.
	unary := []grpc.UnaryServerInterceptor{RecoveryUnary(logger)}
	if cfg.Limiter != nil {
		unary = append(unary, LoadShedUnary(cfg.Limiter))
	}
	unary = append(unary, LoggingUnary(logger), ErrorBoundaryUnary(logger))

	opts := []grpc.ServerOption{
		// Stats handlers replace the removed otelgrpc interceptors and also see
		// transport-level events (message sizes, stream lifetime).
		grpc.StatsHandler(otelgrpc.NewServerHandler(traceOpts...)),
		grpc.ChainUnaryInterceptor(unary...),
		grpc.ChainStreamInterceptor(RecoveryStream(logger), LoggingStream(logger), ErrorBoundaryStream(logger)),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     5 * time.Minute,
			MaxConnectionAge:      30 * time.Minute, // forces reconnects so L4 load balancers rebalance
			MaxConnectionAgeGrace: 30 * time.Second,
			Time:                  time.Minute,
			Timeout:               20 * time.Second,
		}),
		// Must be <= the client's keepalive Time, or clients get GOAWAY
		// "too_many_pings" and see spurious Unavailable errors.
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             15 * time.Second,
			PermitWithoutStream: true,
		}),
		grpc.MaxRecvMsgSize(4 << 20),
	}
	opts = append(opts, cfg.ServerOptions...)

	s := &Server{
		cfg:      cfg,
		grpc:     grpc.NewServer(opts...),
		health:   health.NewServer(),
		draining: make(chan struct{}),
		logger:   logger,
	}
	healthpb.RegisterHealthServer(s.grpc, &drainingHealth{Server: s.health, draining: s.draining})
	if cfg.EnableReflection {
		reflection.Register(s.grpc)
	}
	return s
}

// RegisterService implements grpc.ServiceRegistrar.
func (s *Server) RegisterService(desc *grpc.ServiceDesc, impl any) {
	s.grpc.RegisterService(desc, impl)
}

// Draining is closed when shutdown is about to call GracefulStop, after the
// drain delay. Long-lived streams select on it and return, because
// GracefulStop waits for handlers but never cancels their contexts.
func (s *Server) Draining() <-chan struct{} { return s.draining }

// Health exposes the health server, e.g. to report NOT_SERVING on the ""
// service while a critical dependency is down.
func (s *Server) Health() *health.Server { return s.health }

// Serve listens on cfg.Addr and blocks until ctx is cancelled, then shuts
// down. It returns nil after a clean shutdown.
func (s *Server) Serve(ctx context.Context) error {
	var lc net.ListenConfig
	lis, err := lc.Listen(ctx, "tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.Addr, err)
	}
	return s.ServeListener(ctx, lis)
}

// ServeListener is Serve on an existing listener (bufconn in tests).
func (s *Server) ServeListener(ctx context.Context, lis net.Listener) error {
	for name := range s.grpc.GetServiceInfo() {
		s.health.SetServingStatus(name, healthpb.HealthCheckResponse_SERVING)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- s.grpc.Serve(lis) }()
	s.logger.InfoContext(ctx, "grpc server listening", "addr", lis.Addr().String())

	select {
	case err := <-errCh: // Serve failed before shutdown was requested
		s.grpc.Stop()
		return fmt.Errorf("grpc serve: %w", err)
	case <-ctx.Done():
	}
	s.shutdown()
	// ErrServerStopped means shutdown won the race against Serve starting
	// (e.g. SIGTERM during boot); that is still a clean exit.
	if err := <-errCh; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		return fmt.Errorf("grpc serve: %w", err)
	}
	return nil
}

// shutdown runs the drain sequence:
//  1. health → NOT_SERVING so probes and load balancers stop sending traffic;
//  2. wait DrainDelay for them to notice;
//  3. close Draining so streams (including health Watch) return;
//  4. GracefulStop: refuse new RPCs, wait for in-flight ones;
//  5. Stop if that exceeds ShutdownTimeout.
func (s *Server) shutdown() {
	s.health.Shutdown()
	if s.cfg.DrainDelay > 0 {
		time.Sleep(s.cfg.DrainDelay)
	}
	close(s.draining)

	stopped := make(chan struct{})
	go func() {
		s.grpc.GracefulStop()
		close(stopped)
	}()

	timer := time.NewTimer(s.cfg.ShutdownTimeout)
	defer timer.Stop()
	select {
	case <-stopped:
		s.logger.Info("grpc server stopped gracefully")
	case <-timer.C:
		s.logger.Warn("graceful stop timed out, forcing close", "timeout", s.cfg.ShutdownTimeout)
		s.grpc.Stop()
		<-stopped
	}
}
