# Production checklist

Run before a service ships and when reviewing one for high load. Each item names the failure it prevents; details live in the other playbooks.

## Build and dependencies

- [ ] CI and Dockerfile use a supported toolchain ≥ the go.mod `go` line (older triggers a download or fails).
- [ ] Versions chosen by `govulncheck`: newest *patched*, not newest. `otel*` modules move together.
- [ ] CI runs `go vet`, `golangci-lint run` (team policy), `go test -race -shuffle=on -count=1 ./...`, alloc budgets without `-race` (`go test -run 'Allocs$'`), `govulncheck`, `buf lint`, `buf breaking --against '.git#branch=main'`.
- [ ] Static binary (`CGO_ENABLED=0`, `-trimpath`, version via `-ldflags -X`) on distroless non-root (`assets/service-skeleton/Dockerfile`).

## Concurrency and memory

- [ ] Every goroutine has an owner, an exit path and a bound; fan-out uses `SetLimit`, a pool or a semaphore.
- [ ] Every blocking channel op selects on `ctx.Done()`.
- [ ] Every cache, queue, request-keyed map and buffer has a size cap (and TTL where relevant).
- [ ] Nothing started from a handler keeps the request ctx; detached work has its own timeout and an owner shutdown waits on.
- [ ] `goleak` in every package with goroutines; a leak test under load returns to baseline.
- [ ] Goroutine count, live heap and RSS are graphed per instance.

## Overload and timeouts

- [ ] Timeout chain validated at boot: LB idle < `IdleTimeout`; `WriteTimeout` > request deadline > downstream > queue wait; shutdown budget < grace period.
- [ ] One process-wide limiter for HTTP and gRPC, sized by Little's law; health checks exempt; streams capped by count.
- [ ] Retries: idempotent only, `UNAVAILABLE`/502–504, one layer, jitter, budget. Writes carry idempotency keys.
- [ ] One circuit breaker per dependency; domain errors don't trip it.

## gRPC

- [ ] Stats handler for OTel; interceptors recover → shed → log → error boundary → auth → validate.
- [ ] Domain returns neutral kinds; one boundary maps codes; downstream codes translated, never forwarded.
- [ ] Keepalive: `MaxConnectionAge` set; server `MinTime` ≤ client `Time`.
- [ ] Streams exit on client cancel, source close and `draining`; shutdown: health `NOT_SERVING` → drain delay → `GracefulStop` with timeout → `Stop` → flush telemetry.
- [ ] Clients: `grpc.NewClient`, one shared conn per target, deadline from the incoming ctx, `round_robin` with a headless Service (or a mesh).
- [ ] Reflection off in production.

## API contract

- [ ] `org.svc.v1` package, Request/Response per RPC, `_UNSPECIFIED` enum zero, removed fields `reserved` (number and name).
- [ ] Page tokens, field masks on updates, request IDs on creates; protovalidate rules enforced by an interceptor.

## Security

- [ ] TLS 1.3, mTLS between services (or the mesh does it), cert reload; partial TLS config fails startup.
- [ ] Metadata is untrusted; identity from verified tokens or peer certs; `authorization` never logged.
- [ ] Message, header and body size limits; `ReadHeaderTimeout` on every HTTP server.
- [ ] pprof/admin on a private port only; client IPs only from trusted proxies.
- [ ] No PII or secrets in spans, baggage or logs (`slog.LogValuer`).

## Observability

- [ ] Resource has `service.name`, `service.version`, `deployment.environment.name`; W3C propagator installed; exporter and sampler via `OTEL_*` env.
- [ ] Span names and labels are route patterns; telemetry flushed last at shutdown.
- [ ] JSON logs through `*Context` with `trace_id`/`span_id`; errors and slow requests only.
- [ ] Alerts on server-fault codes (`Internal`, `Unavailable`, `DeadlineExceeded`) and latency SLOs, not on `NotFound`/`InvalidArgument`.

## Kubernetes

- [ ] `GOMEMLIMIT` ≈ 90% of the memory limit; CPU requests set, CPU limit absent or generous; no `automaxprocs`.
- [ ] Probes `httpGet` on the admin port (`/livez`, `/readyz`); liveness never checks dependencies.
- [ ] `readOnlyRootFilesystem`, `runAsNonRoot`, PodDisruptionBudget for replicas > 1.
- [ ] Canary or blue-green, watching error rate and p99 per `service.version`.
- [ ] Load test with an open model; run at ≤70% of the knee; 30–60 min soak shows heap and goroutines plateau.
