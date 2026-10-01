// Command server runs the service: public REST (chi) and gRPC on separate
// ports, plus a private admin port for probes and pprof.
//
// Environment (defaults in parentheses):
//
//	HTTP_ADDR (:8080)  GRPC_ADDR (:50051)  ADMIN_ADDR (:9090, never expose publicly)
//	MAX_INFLIGHT (256)  QUEUE_WAIT (25ms)  shared HTTP+gRPC load shedding
//	HTTP_REQUEST_DEADLINE (5s)  HTTP_WRITE_TIMEOUT (10s)  HTTP_LOG_ALL (false)
//	DRAIN_DELAY (0; 5s behind a load balancer)  SHUTDOWN_BUDGET (28s, < terminationGracePeriod)
//	HTTP_SHUTDOWN_TIMEOUT (10s)  GRPC_SHUTDOWN_TIMEOUT (15s)
//	GRPC_REFLECTION, TRACE_HEALTH_CHECKS (dev only)
//	TLS_CERT_FILE, TLS_KEY_FILE, TLS_CA_FILE  gRPC mTLS: all three or none
//	GOMEMLIMIT  set to ~90% of the container memory limit
//	OTEL_*      standard OpenTelemetry SDK variables
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-chi/chi/v5"

	"example.com/skeleton/internal/admin"
	"example.com/skeleton/internal/envconfig"
	"example.com/skeleton/internal/grpcserver"
	"example.com/skeleton/internal/httpserver"
	"example.com/skeleton/internal/resilience"
	"example.com/skeleton/internal/telemetry"
)

// version is set at build time: go build -ldflags "-X main.version=1.2.3".
var version = "dev"

// main only converts run's result into an exit code: os.Exit skips deferred
// calls, so it must not be reached while any defer is pending.
func main() { os.Exit(realMain()) }

func realMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// After the first signal, restore default handling: a second Ctrl-C or
	// SIGTERM then force-quits a drain that is stuck.
	context.AfterFunc(ctx, stop)

	logger := slog.New(telemetry.NewTraceHandler(slog.NewJSONHandler(os.Stdout, nil)))
	slog.SetDefault(logger)

	if err := run(ctx, logger); err != nil {
		logger.ErrorContext(ctx, "server exited with error", "err", err)
		return 1
	}
	return 0
}

// run holds all startup logic so every error flows back to one exit point
// and every deferred cleanup runs.
func run(ctx context.Context, logger *slog.Logger) error {
	cfg, err := loadConfig(ctx, logger)
	if err != nil {
		return err
	}

	shutdownTelemetry, err := telemetry.Setup(ctx, telemetry.Config{
		ServiceName:    envconfig.String("SERVICE_NAME", "skeleton-server"),
		ServiceVersion: version,
		Environment:    envconfig.String("DEPLOY_ENV", "dev"),
	})
	if err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}
	// Flush telemetry last: ctx is already cancelled by then, and spans from
	// the final in-flight requests are still sitting in the batch processor.
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), telemetryFlushBudget)
		defer cancel()
		if err := shutdownTelemetry(flushCtx); err != nil {
			logger.ErrorContext(flushCtx, "telemetry shutdown", "err", err)
		}
	}()

	return serve(ctx, logger, cfg, newServers(cfg, logger))
}

// newServers wires the application. This is the only place that knows about
// every component: constructors receive their dependencies explicitly, so
// tests can build the same graph with fakes.
func newServers(cfg config, logger *slog.Logger) servers {
	limiter := resilience.NewLimiter(cfg.MaxInFlight, cfg.QueueWait) // one budget for both transports

	grpcCfg := cfg.GRPC
	grpcCfg.Limiter = limiter
	grpcSrv := grpcserver.New(grpcCfg, logger)
	// Register gRPC services here; grpcSrv is a grpc.ServiceRegistrar. Give
	// streaming services grpcSrv.Draining() so they end before GracefulStop.

	// drain is closed by serve() right before the servers stop, so streaming
	// handlers (SSE, long polls) return instead of holding up graceful shutdown.
	drain := make(chan struct{})
	handler := httpserver.NewHandler(logger, limiter, cfg.Handler, httpserver.Routes{
		API: func(_ chi.Router) {
			// Mount REST routes here, e.g. r.Get("/v1/orders/{id}", orders.Get).
		},
		Streams: func(_ chi.Router) {
			// Mount streaming routes here and give them drain, e.g. r.Get("/v1/events", events.Stream(feed, drain)).
		},
	})

	return servers{
		admin: admin.New(cfg.Admin, logger),
		http:  httpserver.New(cfg.HTTP, handler, logger),
		grpc:  grpcSrv,
		drain: drain,
	}
}
