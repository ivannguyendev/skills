// Package telemetry wires OpenTelemetry tracing and metrics for the service.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// Config identifies the service. Exporter endpoint, headers, TLS and sampling
// are read from the standard OTEL_* environment variables so operators can
// change them without a rebuild, e.g.:
//
//	OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4317
//	OTEL_EXPORTER_OTLP_INSECURE=true
//	OTEL_TRACES_SAMPLER=parentbased_traceidratio
//	OTEL_TRACES_SAMPLER_ARG=0.1
type Config struct {
	ServiceName    string
	ServiceVersion string
	Environment    string // e.g. "prod", "staging", "dev"
}

// ShutdownFunc flushes buffered telemetry and releases exporters.
// Call it after servers have stopped, with a context that is not cancelled.
type ShutdownFunc func(context.Context) error

// Setup installs the global tracer provider, meter provider and W3C
// propagators. Globals are only set once every step succeeded; on failure,
// anything already started is shut down. The returned func is idempotent.
func Setup(ctx context.Context, cfg Config) (ShutdownFunc, error) {
	var shutdowns []func(context.Context) error
	var once sync.Once
	var shutdownErr error
	shutdownAll := func(ctx context.Context) error {
		once.Do(func() {
			for _, shutdown := range slices.Backward(shutdowns) {
				shutdownErr = errors.Join(shutdownErr, shutdown(ctx))
			}
		})
		return shutdownErr
	}
	fail := func(err error) (ShutdownFunc, error) {
		// ctx may already be cancelled (signal during boot); clean up anyway.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return nil, errors.Join(err, shutdownAll(cleanupCtx))
	}

	res, err := newResource(ctx, cfg)
	if err != nil {
		return fail(fmt.Errorf("telemetry resource: %w", err))
	}

	// Exporters connect lazily, so a missing collector never blocks startup.
	traceExp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return fail(fmt.Errorf("trace exporter: %w", err))
	}
	// No WithSampler: the SDK honours OTEL_TRACES_SAMPLER and defaults to
	// ParentBased(AlwaysSample), which keeps upstream sampling decisions.
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(traceExp),
		sdktrace.WithResource(res),
	)
	shutdowns = append(shutdowns, tp.Shutdown)

	metricExp, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return fail(fmt.Errorf("metric exporter: %w", err))
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp,
			sdkmetric.WithInterval(30*time.Second))),
		sdkmetric.WithResource(res),
	)
	shutdowns = append(shutdowns, mp.Shutdown)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	// Without a global propagator, otelgrpc/otelhttp neither read nor write
	// traceparent headers and every service starts a disconnected trace.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	// Route export failures into the structured log instead of the std logger.
	otel.SetErrorHandler(otelErrors{})

	return shutdownAll, nil
}

// otelErrors routes SDK export failures into the structured log instead of
// the standard logger.
type otelErrors struct{}

func (otelErrors) Handle(err error) { slog.Warn("opentelemetry error", "err", err) }

// newResource describes this process. Code defaults come first so that
// OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES (applied later) override them.
func newResource(ctx context.Context, cfg Config) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{semconv.ServiceName(cfg.ServiceName)}
	if cfg.ServiceVersion != "" {
		attrs = append(attrs, semconv.ServiceVersion(cfg.ServiceVersion))
	}
	if cfg.Environment != "" {
		attrs = append(attrs, semconv.DeploymentEnvironmentNameKey.String(cfg.Environment))
	}
	return resource.New(ctx,
		resource.WithAttributes(attrs...),
		resource.WithTelemetrySDK(),
		resource.WithHost(),
		resource.WithFromEnv(),
	)
}
