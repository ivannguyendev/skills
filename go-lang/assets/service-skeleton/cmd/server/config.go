package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"google.golang.org/grpc"

	"example.com/skeleton/internal/envconfig"
	"example.com/skeleton/internal/grpcserver"
	"example.com/skeleton/internal/httpserver"
	"example.com/skeleton/internal/tlsconfig"
)

// telemetryFlushBudget is reserved at the end of shutdown for exporting the
// last spans and metrics.
const telemetryFlushBudget = 5 * time.Second

// config is everything the process needs, loaded once at startup. Invalid
// combinations fail here, at boot, instead of as a timeout in production.
type config struct {
	GRPC           grpcserver.Config
	HTTP           httpserver.Config
	Admin          httpserver.Config
	Handler        httpserver.HandlerConfig
	MaxInFlight    int           // shared HTTP + gRPC concurrency (Little's law × headroom)
	QueueWait      time.Duration // how long a request may wait for a slot before 503/UNAVAILABLE
	DrainDelay     time.Duration // NOT_SERVING → stop, so load balancers deregister the pod first
	ShutdownBudget time.Duration // keep below the orchestrator's termination grace period
}

func loadConfig(ctx context.Context, logger *slog.Logger) (config, error) {
	var errs []error
	dur := func(key string, def time.Duration) time.Duration {
		d, err := envconfig.Duration(key, def)
		errs = append(errs, err)
		return d
	}
	flag := func(key string) bool {
		b, err := envconfig.Bool(key, false)
		errs = append(errs, err)
		return b
	}
	maxInFlight, err := envconfig.Int("MAX_INFLIGHT", 256)
	errs = append(errs, err)
	tlsOpts, err := transportOptions(ctx, logger)
	errs = append(errs, err)

	cfg := config{
		GRPC: grpcserver.Config{
			Addr:              envconfig.String("GRPC_ADDR", ":50051"),
			ShutdownTimeout:   dur("GRPC_SHUTDOWN_TIMEOUT", 15*time.Second),
			EnableReflection:  flag("GRPC_REFLECTION"),
			TraceHealthChecks: flag("TRACE_HEALTH_CHECKS"),
			ServerOptions:     tlsOpts,
			// DrainDelay stays 0: the process-level drain in serve() covers HTTP and gRPC at once.
		},
		HTTP: httpserver.Config{
			Addr:            envconfig.String("HTTP_ADDR", ":8080"),
			WriteTimeout:    dur("HTTP_WRITE_TIMEOUT", 10*time.Second),
			ShutdownTimeout: dur("HTTP_SHUTDOWN_TIMEOUT", 10*time.Second),
		},
		// The admin port serves unauthenticated pprof. Kubelet probes need it on
		// the pod network, so restrict it with a NetworkPolicy and never publish
		// it through a Service, an Ingress or `docker run -p`.
		Admin: httpserver.Config{Addr: envconfig.String("ADMIN_ADDR", ":9090"), ShutdownTimeout: 2 * time.Second},
		Handler: httpserver.HandlerConfig{
			Deadline: dur("HTTP_REQUEST_DEADLINE", 5*time.Second),
			LogAll:   flag("HTTP_LOG_ALL"),
		},
		MaxInFlight:    maxInFlight,
		QueueWait:      dur("QUEUE_WAIT", 25*time.Millisecond),
		DrainDelay:     dur("DRAIN_DELAY", 0),
		ShutdownBudget: dur("SHUTDOWN_BUDGET", 28*time.Second),
	}
	if err := errors.Join(errs...); err != nil {
		return config{}, fmt.Errorf("config: %w", err) // parse errors make validate's checks meaningless
	}
	if err := cfg.validate(); err != nil {
		return config{}, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

// validate enforces the timeout hierarchy. Each rule prevents a specific
// production failure, noted inline.
func (c config) validate() error {
	var errs []error
	if c.MaxInFlight < 1 {
		errs = append(errs, fmt.Errorf("MAX_INFLIGHT must be at least 1, got %d", c.MaxInFlight))
	}
	for name, d := range map[string]time.Duration{
		"QUEUE_WAIT": c.QueueWait, "DRAIN_DELAY": c.DrainDelay,
	} {
		if d < 0 {
			errs = append(errs, fmt.Errorf("%s must not be negative", name))
		}
	}
	for name, d := range map[string]time.Duration{
		"HTTP_REQUEST_DEADLINE": c.Handler.Deadline, "HTTP_WRITE_TIMEOUT": c.HTTP.WriteTimeout,
		"HTTP_SHUTDOWN_TIMEOUT": c.HTTP.ShutdownTimeout, "GRPC_SHUTDOWN_TIMEOUT": c.GRPC.ShutdownTimeout,
		"SHUTDOWN_BUDGET": c.ShutdownBudget,
	} {
		if d <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", name))
		}
	}
	if c.Handler.Deadline >= c.HTTP.WriteTimeout {
		// The server would cut the connection before the handler's own
		// deadline fires, so clients see resets instead of clean errors.
		errs = append(errs, fmt.Errorf("HTTP_REQUEST_DEADLINE (%v) must be below HTTP_WRITE_TIMEOUT (%v)",
			c.Handler.Deadline, c.HTTP.WriteTimeout))
	}
	if c.QueueWait*10 > c.Handler.Deadline {
		// A long queue wait just moves the timeout into the queue.
		errs = append(errs, errors.New("QUEUE_WAIT must be at most a tenth of HTTP_REQUEST_DEADLINE"))
	}
	if c.HTTP.ShutdownTimeout < c.Handler.Deadline {
		// Shutdown would cut requests that are still within their own budget.
		errs = append(errs, errors.New("HTTP_SHUTDOWN_TIMEOUT must be at least HTTP_REQUEST_DEADLINE"))
	}
	stop := max(c.HTTP.ShutdownTimeout, c.GRPC.ShutdownTimeout)
	if total := c.DrainDelay + stop + c.Admin.ShutdownTimeout + telemetryFlushBudget; total > c.ShutdownBudget {
		// The orchestrator would SIGKILL mid-drain and drop in-flight work.
		errs = append(errs, fmt.Errorf("drain + shutdown + flush = %v exceeds SHUTDOWN_BUDGET %v", total, c.ShutdownBudget))
	}
	return errors.Join(errs...)
}

// transportOptions enables mTLS when all three TLS_* variables are set. A
// partial set is an error rather than a silent fallback to plaintext.
func transportOptions(ctx context.Context, logger *slog.Logger) ([]grpc.ServerOption, error) {
	files := tlsconfig.Files{
		CertFile: os.Getenv("TLS_CERT_FILE"),
		KeyFile:  os.Getenv("TLS_KEY_FILE"),
		CAFile:   os.Getenv("TLS_CA_FILE"),
	}
	set := 0
	for _, v := range []string{files.CertFile, files.KeyFile, files.CAFile} {
		if v != "" {
			set++
		}
	}
	switch set {
	case 0:
		logger.WarnContext(ctx, "serving plaintext gRPC: TLS_* not set (acceptable only locally or behind a mesh)")
		return nil, nil
	case 3:
		creds, err := tlsconfig.ServerCredentials(files)
		if err != nil {
			return nil, fmt.Errorf("mtls: %w", err)
		}
		return []grpc.ServerOption{grpc.Creds(creds)}, nil
	default:
		return nil, fmt.Errorf("mtls: set all of TLS_CERT_FILE, TLS_KEY_FILE, TLS_CA_FILE or none (got %d of 3)", set)
	}
}
