# OpenTelemetry tracing in Go

Instrumenting Go services so requests can be followed across processes. Exporter
and backend setup is in `tracing-backends.md`. The SDK bootstrap you should copy
is `assets/service-skeleton/internal/telemetry/otel_setup.go`.

## Contents

1. [Concepts in one table](#concepts-in-one-table)
2. [Versions and packages](#versions-and-packages)
3. [SDK setup and shutdown](#sdk-setup-and-shutdown)
4. [Resource: who emitted the span](#resource-who-emitted-the-span)
5. [Automatic instrumentation: gRPC and HTTP](#automatic-instrumentation-grpc-and-http)
6. [Manual spans](#manual-spans)
7. [Propagation across async boundaries](#propagation-across-async-boundaries)
8. [Correlating logs](#correlating-logs)
9. [Sampling](#sampling)
10. [Testing instrumentation](#testing-instrumentation)
11. [Pitfalls](#pitfalls)

## Concepts in one table

| Term | Meaning | Practical rule |
|---|---|---|
| Trace | Every span for one request, sharing a 16-byte trace ID | Created once at the edge, then propagated |
| Span | One timed operation: name, kind, attributes, events, status, links | Name it after the *operation*, never with IDs in it |
| Span kind | `SERVER`, `CLIENT`, `PRODUCER`, `CONSUMER`, `INTERNAL` | Instrumentation libraries set it for you; set it yourself for messaging |
| Attributes | Typed key/values on a span or resource | Prefer semconv keys so dashboards and backends understand them |
| Events | Timestamped annotations inside a span | `RecordError` adds an `exception` event |
| Status | `Unset`, `Error`, `Ok` | Set `Error` on failures; leave `Unset` on success |
| Links | References to spans in other traces | Batch consumers, detached work, fan-in |
| Context propagation | Carrying trace context across process boundaries | W3C `traceparent` / `tracestate` headers |
| Baggage | User-defined key/values propagated with the trace | Visible to every downstream service, and to third parties if it leaks. Never put PII or secrets in it |

`traceparent: 00-<trace-id>-<parent-span-id>-<flags>`. The last byte, `01`, means
"sampled". Downstream services that use a `ParentBased` sampler respect it.

## Versions and packages

| Module | Version used here | Note |
|---|---|---|
| `go.opentelemetry.io/otel`, `/sdk`, `/trace`, `/metric` | v1.46.0 | Latest stable as of 2026-10-01. v1.47.0-rc.1 declares `go 1.26.0` |
| `.../exporters/otlp/otlptrace/otlptracegrpc` (or `otlptracehttp`) | v1.46.0 | OTLP is the only exporter you need. The Jaeger exporter was removed long ago |
| `go.opentelemetry.io/otel/semconv/v1.43.0` | ships with SDK v1.46.0 | Use the semconv version your SDK's `resource` package uses |
| `.../contrib/instrumentation/google.golang.org/grpc/otelgrpc` | v0.71.0 | Stats handlers only; interceptors were removed |
| `.../contrib/instrumentation/net/http/otelhttp` | v0.71.0 | |
| `.../contrib/bridges/otelslog` | v0.20.1 | Pre-1.0. Optional |

Upgrade the `otel*` modules together. Mixing minor versions of the API, SDK and
contrib packages is the most common source of compile errors and missing spans.

## SDK setup and shutdown

`telemetry.Setup` in the skeleton does the four things every service needs:

1. **Builds a Resource**, see below.
2. **Creates an OTLP trace exporter with a batching span processor.**
   `WithBatcher` exports asynchronously. `WithSyncer` blocks every span end on
   network I/O, so it's for tests only.
3. **Installs the global TracerProvider and MeterProvider.**
4. **Installs the composite W3C propagator (TraceContext + Baggage).** Without
   it, otelgrpc and otelhttp neither read nor write headers, and every service
   starts its own disconnected trace.

`Setup` then returns a `Shutdown` func. Call it **last**, after servers stop,
with a fresh context and a timeout. Otherwise the spans of the final requests
are still buffered when the process exits.

Exporters are configured through standard environment variables, so the same
binary runs anywhere:

| Variable | Example | Effect |
|---|---|---|
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://otel-collector:4317` | Collector address (gRPC 4317, HTTP 4318) |
| `OTEL_EXPORTER_OTLP_INSECURE` | `true` | Plaintext to a local or sidecar collector |
| `OTEL_EXPORTER_OTLP_HEADERS` | `authorization=Bearer abc` | Vendor auth |
| `OTEL_SERVICE_NAME` | `orders` | Overrides `service.name` |
| `OTEL_RESOURCE_ATTRIBUTES` | `deployment.environment.name=prod,k8s.pod.name=$(POD)` | Extra resource attributes |
| `OTEL_TRACES_SAMPLER` / `_ARG` | `parentbased_traceidratio` / `0.1` | Head sampling, read when no `WithSampler` is passed |
| `OTEL_SEMCONV_STABILITY_OPT_IN` | `rpc/dup` | otelgrpc emits old *and* new RPC attribute names during a dashboard migration |

## Resource: who emitted the span

The resource identifies the process. Always set `service.name`, because
backends group everything by it. `service.version` and
`deployment.environment.name` make "did the deploy cause this?" a one-click
question.

In `resource.New(ctx, opts...)`, later options override earlier ones. The
skeleton therefore lists code defaults first and `resource.WithFromEnv()` last,
so operators win.

**Schema URL trap:** `resource.Merge(a, b)` fails with "conflicting Schema URL"
when both sides carry *different* non-empty schema URLs. That's what happens
when you merge `resource.Default()` (SDK semconv) with a resource built using an
older `semconv/vX` package. Prefer one `resource.New(...)` call with no explicit
schema URL, as the skeleton does. Otherwise import the exact semconv version
your SDK uses.

## Automatic instrumentation: gRPC and HTTP

- **gRPC:** the skeleton installs `otelgrpc.NewServerHandler` /
  `NewClientHandler` as stats handlers.
  - It filters out health checks with `filters.Not(filters.HealthCheck())`,
    because probes every few seconds would dominate trace volume.
  - Each RPC span is named `pkg.Service/Method` and carries
    `rpc.system.name=grpc`, `rpc.method` and `rpc.response.status_code` (for
    example `"NOT_FOUND"`).
  - Those are the *new* RPC semconv names, the default in otelgrpc v0.71.
    Queries and dashboards built on the old `rpc.grpc.status_code` (an int) or
    `rpc.service` need `OTEL_SEMCONV_STABILITY_OPT_IN=rpc/dup` while you migrate.
  - Metrics: `rpc.server.call.duration` and `rpc.client.call.duration`
    histograms.
- **HTTP:** wrap the mux once, and wrap outgoing transports.

```go
// Package httpapi instruments an HTTP API that runs next to gRPC.
package httpapi

import (
	"net/http"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// NewHandler traces every request except probes, for a plain ServeMux.
// With chi, use the skeleton's RouteTag middleware instead: chi records the
// pattern on a copy of the request that otelhttp never sees.
// Span names use the ServeMux pattern ("GET /orders/{id}"), which otelhttp applies after
// routing, keeping names low-cardinality.
func NewHandler(mux *http.ServeMux) http.Handler {
	return otelhttp.NewHandler(mux, "http.server",
		otelhttp.WithFilter(func(r *http.Request) bool { return r.URL.Path != "/livez" }),
		otelhttp.WithSpanNameFormatter(func(op string, r *http.Request) string {
			if r.Pattern != "" {
				return r.Pattern
			}
			return op
		}),
	)
}

// NewClient injects traceparent into every outgoing request.
func NewClient() *http.Client {
	return &http.Client{
		Transport: otelhttp.NewTransport(http.DefaultTransport),
		Timeout:   10 * time.Second,
	}
}
```

## Manual spans

Add spans for work that matters in a latency breakdown: calls into a gateway, a
cache lookup, a CPU-heavy step. Don't add them for every function. Each span
costs allocations and export bandwidth.

```go
// Package payments shows manual span conventions.
package payments

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes" // aliased: grpc also has a "codes" package
	"go.opentelemetry.io/otel/trace"
)

// Name the tracer after the package import path; it becomes the
// instrumentation scope. The global provider is a delegate, so this is safe
// even before telemetry.Setup runs.
var tracer = otel.Tracer("example.com/payments")

// Gateway is the external payment provider.
type Gateway interface {
	Charge(ctx context.Context, cents int64) (txID string, err error)
}

// Charge wraps one gateway call in a span.
func Charge(ctx context.Context, gw Gateway, orderID string, cents int64) (string, error) {
	ctx, span := tracer.Start(ctx, "payments.Charge", // fixed name; IDs go in attributes
		trace.WithAttributes(
			attribute.String("order.id", orderID),
			attribute.Int64("payment.amount_cents", cents),
		))
	defer span.End()

	txID, err := gw.Charge(ctx, cents) // pass ctx so child spans nest correctly
	if err != nil {
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, "gateway charge failed")
		return "", fmt.Errorf("charge order %s: %w", orderID, err)
	}
	span.AddEvent("payment.authorized", trace.WithAttributes(attribute.String("payment.tx_id", txID)))
	return txID, nil
}
```

Rules that keep traces useful:
- **Span names are low-cardinality** (`payments.Charge`,
  `GET /orders/{id}`). Backends index by name, so a name containing an order ID
  creates millions of "operations".
- **Prefer semconv attribute keys** (`semconv.DBSystemNamePostgreSQL`,
  `semconv.ServerAddress`). Use namespaced custom keys (`order.id`) for domain
  data.
- **Record errors where you handle them**, not at every layer they pass
  through.
- **Always `defer span.End()`.** A span that never ends is never exported.

## Propagation across async boundaries

Inside a process, the trace travels in `context.Context`. Pass `ctx` to
goroutines, or for detached work use `context.WithoutCancel(ctx)` plus a span
link (see `concurrency-patterns.md`). Across a message broker, inject into the
message headers and extract on the other side.

```go
// Package messaging propagates trace context through message headers.
package messaging

import (
	"context"

	"go.opentelemetry.io/otel"
	otelcodes "go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// Header mirrors a broker header (Kafka record header, NATS header, ...).
type Header struct{ Key, Value string }

// Message is a broker-agnostic envelope.
type Message struct {
	Topic   string
	Value   []byte
	Headers []Header
}

// carrier adapts message headers to propagation.TextMapCarrier.
type carrier struct{ msg *Message }

func (c carrier) Get(key string) string {
	for _, h := range c.msg.Headers {
		if h.Key == key {
			return h.Value
		}
	}
	return ""
}

func (c carrier) Set(key, value string) {
	for i, h := range c.msg.Headers {
		if h.Key == key {
			c.msg.Headers[i].Value = value
			return
		}
	}
	c.msg.Headers = append(c.msg.Headers, Header{Key: key, Value: value})
}

func (c carrier) Keys() []string {
	keys := make([]string, 0, len(c.msg.Headers))
	for _, h := range c.msg.Headers {
		keys = append(keys, h.Key)
	}
	return keys
}

var tracer = otel.Tracer("example.com/messaging")

// Publish records a PRODUCER span and injects its context into msg.
func Publish(ctx context.Context, send func(context.Context, *Message) error, msg *Message) error {
	ctx, span := tracer.Start(ctx, "send "+msg.Topic,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(semconv.MessagingDestinationName(msg.Topic)))
	defer span.End()

	otel.GetTextMapPropagator().Inject(ctx, carrier{msg})
	if err := send(ctx, msg); err != nil {
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, "send failed")
		return err
	}
	return nil
}

// Consume continues the producer's trace in a CONSUMER span.
func Consume(ctx context.Context, msg *Message, handle func(context.Context, *Message) error) error {
	ctx = otel.GetTextMapPropagator().Extract(ctx, carrier{msg})
	ctx, span := tracer.Start(ctx, "process "+msg.Topic,
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(semconv.MessagingDestinationName(msg.Topic)))
	defer span.End()

	if err := handle(ctx, msg); err != nil {
		span.RecordError(err)
		span.SetStatus(otelcodes.Error, "processing failed")
		return err
	}
	return nil
}
```

When one consumer span processes a *batch* of messages, it can't have many
parents. Start it as a new root and add one `trace.Link` per message context.

## Correlating logs

A log line that carries `trace_id` and `span_id` links straight to its trace.
Two options:

1. **Default: `telemetry.TraceHandler` from the skeleton.** It's a slog
   decorator that reads the span from the context passed to `InfoContext` and
   friends. It has no extra dependencies and works with any log shipper that
   parses JSON.
2. **`otelslog` bridge**
   (`go.opentelemetry.io/contrib/bridges/otelslog`, pre-1.0). It sends records
   through the OTel Logs SDK and an OTLP log exporter, so logs, traces and
   metrics share one pipeline. Choose it when the backend ingests OTLP logs.
   Trace IDs are attached only when you log with a context.

With either option, `slog.Info(...)` without a context cannot be correlated.
Make the `*Context` variants the house style.

## Sampling

| Strategy | Where | Pros | Cons |
|---|---|---|---|
| `ParentBased(AlwaysSample)` (the SDK default) | SDK | Never breaks traces | 100% volume |
| `ParentBased(TraceIDRatioBased(0.1))` | SDK, via `OTEL_TRACES_SAMPLER=parentbased_traceidratio` | Cheap, consistent across services | Drops 90% of errors too |
| Tail sampling (keep errors, keep slow, sample the rest) | Collector `tail_sampling` processor | Keeps every interesting trace | Collector buffers whole traces. Every span of a trace has to reach the same collector instance |

A common production setup:
- In services, use `ParentBased` with a high ratio, or always-on.
- In the Collector, do tail sampling to keep errors and slow requests (see
  `assets/observability/otel-collector-config.yaml`).

Always wrap ratio samplers in `ParentBased`. Without it, each service makes its
own decision and you get traces with holes.

## Testing instrumentation

Inject a `TracerProvider` instead of reading the global one, so tests can see
the spans. otelgrpc and otelhttp take `WithTracerProvider(tp)`.

```go
package spantest

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// Work is the code under test; it takes a tracer instead of using a global.
func Work(ctx context.Context, tracer trace.Tracer, fail bool) error {
	_, span := tracer.Start(ctx, "work")
	defer span.End()
	if fail {
		err := errors.New("boom")
		span.RecordError(err)
		span.SetStatus(codes.Error, "work failed")
		return err
	}
	return nil
}

func TestWorkMarksFailures(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	if err := Work(t.Context(), tp.Tracer("test"), true); err == nil {
		t.Fatal("expected error")
	}

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if got := spans[0].Status().Code; got != codes.Error {
		t.Errorf("status = %v, want Error", got)
	}
	if ev := spans[0].Events(); len(ev) != 1 || ev[0].Name != "exception" {
		t.Errorf("events = %v, want one exception event", ev)
	}
}
```

## Pitfalls

| Symptom | Cause | Fix |
|---|---|---|
| Each service shows its own one-span traces | No global propagator, or a hop that isn't instrumented | `otel.SetTextMapPropagator(TraceContext+Baggage)`. Instrument every client and server |
| Spans missing after deploys or restarts | `Shutdown` not called, or called with an already-cancelled ctx | Flush last, with `context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)` |
| `conflicting Schema URL` at startup | Merging resources built with different semconv versions | One `resource.New` call, or match the SDK's semconv version |
| `undefined: semconv.X` after an upgrade | semconv packages are versioned and some attributes are enums (`semconv.DeploymentEnvironmentNameKey.String(...)`) | Compile against the semconv version you import, and read its `attribute_group.go` |
| Millions of operations in the backend | IDs in span names | Fixed names, IDs in attributes |
| Child spans attached to the wrong parent | Started a span but kept passing the *old* ctx | Always continue with the ctx returned by `tracer.Start` |
| Trace volume dominated by probes | Health checks traced | `filters.Not(filters.HealthCheck())` / an `otelhttp.WithFilter` |
| `otel/codes` vs `grpc/codes` confusion | Same package name | Alias one: `otelcodes "go.opentelemetry.io/otel/codes"` |
| PII in a third party's traces | Baggage or attributes propagated out of your trust zone | Strip baggage at the edge. Never put secrets or PII in either |
