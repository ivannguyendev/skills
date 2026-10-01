# Production checklist

Use this list before a service ships and when reviewing someone else's. Each
item names the failure it prevents. Details are in the other references.

## Build and dependencies

- [ ] CI and the Dockerfile build with a *supported* toolchain at or above the
      `go` line in go.mod. The go line is a minimum and sets language
      semantics such as loop variables and timers. A toolchain older than it
      triggers a toolchain download or fails the build.
- [ ] Dependencies are pinned and upgraded deliberately, with the `otel*`
      modules moving together. `govulncheck` decides the version: the newest
      *patched* release, which is not always the newest release (grpc-go v1.83.2
      rather than v1.84.0 as of 2026-10).
- [ ] CI runs:
  - `go vet`
  - `go test -race -count=1 ./...`
  - `govulncheck ./...`
  - `buf lint`
  - `buf breaking --against '.git#branch=main'`
  - `golangci-lint run` with the team policy (`assets/service-skeleton/.golangci.yaml`)
  - allocation-budget tests (`go test -run Allocs`, without `-race`)
- [ ] Static binary (`CGO_ENABLED=0`, `-trimpath`), version stamped with
      `-ldflags -X`, on a distroless non-root image
      (`assets/service-skeleton/Dockerfile`).

## Concurrency and memory

- [ ] Every goroutine has an owner, an exit path and a bound. Fan-out uses
      `errgroup.SetLimit`, a worker pool or a semaphore.
- [ ] Every blocking channel operation also selects on `ctx.Done()`.
- [ ] Every cache, queue, map keyed by request data and client buffer has a
      size limit and, where relevant, a TTL.
- [ ] `goleak.VerifyTestMain` runs in every package with goroutines, and a
      leak test under load shows goroutines return to baseline.
- [ ] One process-wide `resilience.Limiter` sheds load for HTTP and gRPC, and
      health checks are exempt from it.
- [ ] Nothing started from a handler keeps using the request ctx after the
      handler returns. Use `context.WithoutCancel` with a timeout and an owner
      that waits for it.
- [ ] The goroutine count is graphed. A count that only rises is a leak.

## gRPC server

- [ ] Stats handler for traces and metrics. Interceptors in the order recovery →
      logging → error boundary → auth → validation.
- [ ] The domain returns `apperr` kinds. Only the boundary turns them into
      status codes, and no raw error text reaches clients.
- [ ] Keepalive set:
  - `MaxConnectionAge` so load balancers can rebalance.
  - `EnforcementPolicy.MinTime` ≤ the client's keepalive `Time`.
- [ ] Streaming handlers exit on client cancel, on source close *and* on
      `srv.Draining()`. Don't use the SIGTERM ctx, which fires before the drain
      delay.
- [ ] Health is registered and every service is marked SERVING. Reflection is
      off in production.
- [ ] Shutdown order: health NOT_SERVING → drain delay → `GracefulStop` with a
      timeout → `Stop` → flush telemetry. `terminationGracePeriodSeconds`
      covers the total.

## gRPC client

- [ ] Use `grpc.NewClient`, one connection per target, shared, and closed after
      the server stops.
- [ ] Every call has a deadline derived from the incoming ctx. Never use
      `context.Background()` inside a handler.
- [ ] Retries are configured only for idempotent methods, on `UNAVAILABLE`.
      Writes carry idempotency keys.
- [ ] `round_robin` with a headless Service (`dns:///…`), or a mesh, so load
      actually spreads.
- [ ] Downstream status codes are *translated*, not forwarded.

## API contract

- [ ] Versioned package (`org.svc.v1`), a Request/Response pair per RPC,
      `_UNSPECIFIED` enum zero values, `reserved` for removed fields.
- [ ] Pagination tokens, field masks on updates, idempotency keys on creates.
- [ ] protovalidate rules in the schema, enforced by an interceptor and covered
      by tests.

## Security

- [ ] TLS 1.3 everywhere, mTLS between services, leaf certificates reloaded on
      rotation. A partial TLS configuration fails startup instead of serving
      plaintext.
- [ ] Metadata is untrusted. Identity comes from verified tokens or peer
      certificates, and `authorization` values are never logged.
- [ ] Message and header size limits are set, and long-running work has its own
      timeouts.
- [ ] pprof and admin endpoints listen on a private address only. Every HTTP
      server sets `ReadHeaderTimeout`.
- [ ] No PII or secrets in span attributes, baggage or logs. Use a
      `slog.LogValuer` for redaction.

## Observability

- [ ] `service.name`, `service.version` and `deployment.environment.name` are on
      the resource, and the W3C propagator is installed.
- [ ] Exporter and sampler are configured through `OTEL_*` env vars. Telemetry
      is flushed at shutdown.
- [ ] Logs are JSON, written through `*Context` methods, and carry
      `trace_id`/`span_id`.
- [ ] Dashboards and alerts use the new RPC semconv names
      (`rpc.response.status_code`), or `OTEL_SEMCONV_STABILITY_OPT_IN=rpc/dup`
      is set during the migration.
- [ ] Alerts fire on server-fault codes (`Internal`, `Unavailable`,
      `DeadlineExceeded`, …) and latency SLOs, not on `NotFound` or
      `InvalidArgument`.

## Deployment (Kubernetes)

- [ ] Native gRPC readiness and liveness probes. Liveness never checks
      dependencies. With app-level mTLS, kubelet probes can't handshake, so use
      a plaintext health-only port, `grpc_health_probe` with certs, or
      mesh-terminated TLS.
- [ ] Set `GOMEMLIMIT` to about 90% of the container memory limit, so the GC
      works harder before the OOM killer acts. GOMAXPROCS already follows the CPU
      limit on Go 1.25+, so remove `automaxprocs`.
- [ ] `readOnlyRootFilesystem: true`, `runAsNonRoot: true`, resource requests
      set, and a PodDisruptionBudget for replicas > 1.
- [ ] Canary or blue-green rollout, watching gRPC error rate and latency per
      `service.version`.

## Top anti-patterns at a glance

| Anti-pattern | Consequence | Reference |
|---|---|---|
| `grpc.Dial` / `WithBlock` | Deprecated API. Deploy-order coupling and crash loops | grpc-client-and-testing.md |
| New `ClientConn` per request | Socket exhaustion, TLS handshake on every call | grpc-client-and-testing.md |
| otelgrpc interceptors, Jaeger exporter | Removed APIs; the code doesn't compile on current versions | opentelemetry-go.md |
| Unselected channel send in a worker | Goroutine leak once the reader stops | concurrency-patterns.md |
| `int` hash `% n` for sharding | Negative index → panic | concurrency-patterns.md |
| `go f(ctx)` from a handler | Work cancelled at random when the RPC returns | concurrency-patterns.md |
| Raw `err.Error()` to clients | Leaks SQL, hosts and tenant data | grpc-server.md |
| Forwarding downstream status codes | Your bug reported as the caller's fault | grpc-client-and-testing.md |
| Streams that ignore shutdown | `GracefulStop` hangs until the hard stop | grpc-server.md |
| IDs in span names | Backend cardinality explosion | opentelemetry-go.md |
| No `Shutdown` flush | The last requests' spans are lost | opentelemetry-go.md |
| Reusing a proto field number | Silent data corruption | protobuf-and-buf.md |
