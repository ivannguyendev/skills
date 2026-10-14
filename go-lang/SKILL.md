---
name: go-lang
description: "Production Go services: architecture, goroutine lifecycle, bounded resources, timeouts, load shedding, circuit breakers, GOMEMLIMIT, sync.Pool, leak hunting, gRPC (Buf, mTLS, OpenTelemetry), tracing backends (Jaeger, Tempo), team code style and HTTP framework choice (chi, gin, echo, fiber). Use whenever writing, reviewing, designing or debugging a Go server, API, worker or microservice, even without 'enterprise': enterprise go, go microservice, production go, go architecture, chuẩn code go, tối ưu hiệu năng go, viết server go chịu tải cao, bị leak goroutine, RAM tăng dần, OOMKilled, go load shedding, gRPC go enterprise, chọn framework go. Not for other languages or small CLIs."
metadata:
  version: "1.0.0"
  date-added: "2026-10-04"
  source: "self"
  tested-with: "go1.27.1 and go1.26.8 (module floor go 1.26.0), grpc-go v1.83.2, otel-go v1.46.0, otelgrpc/otelhttp v0.71.0, chi v5.3.2, golangci-lint v2.14.0, buf 1.73.0"
---

# Go Enterprise Playbook

Go servers that keep latency and memory flat under load, shut down cleanly and never leak. Load a playbook only when the task needs it.

Start new services from `assets/service-skeleton` (see [Skeleton](#skeleton)) rather than writing from memory: Go APIs changed a lot in 2023–2026, and code from memory tends to call removed functions.

## Hard rules

These apply to all code and advice. To break one, give a one-line reason (e.g. the mesh already does mTLS).

**Lifecycle**

1. Every goroutine has an owner that can stop and wait for it, plus an exit path. No `go f(r.Context())` in handlers: that ctx dies on return.
2. Constructors never start goroutines. Expose `Run(ctx) error`; `main` sequences start and stop.
3. Every channel send and receive sits in a `select` with `<-ctx.Done()`. Defer every `cancel`. Close bodies, rows, streams and tickers on every path.

**Bounds and timeouts**

4. No `http.Server{}`, `http.ListenAndServe` or `http.DefaultClient`, because they have no timeouts. Set ReadHeader/Read/Write/Idle timeouts, and use one tuned client per dependency.
5. Every queue, cache, request-keyed map, growing slice, request body and retry count has a fixed limit.

**Overload**

6. One process-wide limiter for HTTP and gRPC. When no slot frees up within a short queue wait, return 503 + `Retry-After` or `UNAVAILABLE`, never an unbounded queue. Health checks and streams are exempt.
7. One circuit breaker per dependency; domain errors don't trip it. Retry at one layer only, for idempotent calls, with jitter and a budget.

**Memory**

8. `GOMEMLIMIT` ≈ 90% of the container limit, `GOGC=100`. Without it, the OOM killer acts before the GC does.
9. `sync.Pool`: pool pointers, Reset them, keep no reference after Put, drop oversized objects.

**Security and observability**

10. Span names and metric labels use route patterns (`GET /users/{id}`), never raw paths or IDs. Unbounded cardinality is a memory leak.
11. Service-to-service gRPC uses mTLS (TLS 1.3, `RequireAndVerifyClientCert`, private CA); a partial TLS config fails startup. pprof, metrics and probes go on a separate admin port, never the public listener or `DefaultServeMux`.

## Outdated → current

| Seen                                  | Use                                              |
| ------------------------------------- | ------------------------------------------------ |
| `grpc.Dial`/`DialContext`/`WithBlock` | `grpc.NewClient`, never block at boot            |
| otelgrpc interceptors                 | `grpc.StatsHandler(otelgrpc.NewServerHandler())` |
| Jaeger exporter                       | OTLP → Collector                                 |
| chi `RealIP`                          | `ClientIPFromXFFTrustedProxies` (spoof-safe)     |
| `automaxprocs`, heap ballast          | nothing (Go ≥1.25), `GOMEMLIMIT`                 |
| `wg.Add(1); go …`                     | `wg.Go(f)` (1.25)                                |
| `errors.As(err, &t)`                  | `errors.AsType[*T](err)` (1.26)                  |
| `for i := 0; i < b.N; i++`            | `for b.Loop()` (1.24)                            |
| retry on `DEADLINE_EXCEEDED`          | `UNAVAILABLE` only                               |

Use an API only if the repo's `go` line allows it.

## Workflow

1. Read the `go.mod` go line, the grpc/otel/router versions and existing patterns. Run `govulncheck` before you pick versions: the newest release isn't always patched.
2. Load profile: RPS per instance, p99, memory/CPU limit, mesh or not. Ask ≤3 questions, only for what the code can't tell you; otherwise state assumptions. Size limits as RPS × p99 × 2–3.
3. Load 1 playbook, 2 at most.
4. End with a verification step: `-race`, `goleak`, a pprof diff, `buf breaking`, or a load test plus the metric to watch.

**New service:** copy and rename the skeleton → write down the load profile and set `MAX_INFLIGHT`, timeouts and pool sizes from it → add a domain package with consumer-side interfaces → adapters wired in `newServers` (`cmd/server/main.go`) → REST routes in the `NewHandler` callback, gRPC services on `grpcserver.Server` (streams watch `Draining()`) → tests: fakes, then `httptest`/`bufconn` through the real chain, then a leak test like `internal/httpserver/leak_test.go` → `golangci-lint`, `go test -race`, `govulncheck`.

**Proto change:** additive only → `reserved` for removals → `buf breaking` before merge → `v2` beside `v1` for a redesign (`playbooks/grpc-production.md`).

## Playbooks

| Task / symptom                                                                                 | File                                   |
| ---------------------------------------------------------------------------------------------- | -------------------------------------- |
| New service, layout, monolith vs services, HTTP framework choice, wiring, shutdown             | `playbooks/architecture-and-layout.md` |
| Fan-out, worker pool, background work, singleflight, races                                     | `playbooks/concurrency-safety.md`      |
| Timeouts, HTTP hardening, shedding, breakers, retries, sizing                                  | `playbooks/resilience-and-load.md`     |
| RAM growth, OOMKilled, goroutine leak, GC/p99, profiling, pool                                 | `playbooks/memory-and-performance.md`  |
| gRPC server/client, Buf, mTLS, streaming, health, OTel                                         | `playbooks/grpc-production.md`         |
| Collector, Jaeger v2, Tempo, TraceQL, "no traces"                                              | `playbooks/tracing-backends.md`        |
| Naming, packages, interfaces, errors, logging, tests, lint, review checklist, version features | `playbooks/code-style-and-review.md`   |
| Pre-ship or high-load readiness review                                                         | `playbooks/production-checklist.md`    |

For a review, check the hard rules first, then open the playbook for each area that fails.

## Skeleton

`assets/service-skeleton/` is a Go module that builds, lints with zero issues against its own `.golangci.yaml`, and passes `go test -race` including leak tests and allocation budgets (versions in `metadata.tested-with`).

```text
cmd/server/            main → config (validated timeout chain) → wiring (newServers) → lifecycle (ordered shutdown)
internal/httpserver/   hardened http.Server, chi chain (RouteTag, Recover, AccessLog, LoadShed, Deadline, BodyLimit), WriteJSON/DecodeJSON
internal/grpcserver/   keepalive, health (Watch ends on drain), interceptors, load shedding, error mapping
internal/grpcclient/   NewClient factory: service config with retryThrottling, keepalive, downstream-error wrapping
internal/admin/        private port: /livez, /readyz, pprof
internal/resilience/   process-wide limiter (0 allocs)
internal/telemetry/    OTel SDK setup, slog trace handler
internal/tlsconfig/    mTLS with cert reload
internal/apperr/       transport-neutral error kinds
internal/envconfig/    typed env parsing
.golangci.yaml         team lint policy
Dockerfile             golang:1.27 → static binary on distroless nonroot
```

```sh
cp -r <skill-dir>/assets/service-skeleton ~/src/orders && cd ~/src/orders
go mod edit -module github.com/acme/orders
grep -rl 'example.com/skeleton' --include='*.go' --include='.golangci.yaml' . \
  | xargs perl -pi -e 's#example\.com/skeleton#github.com/acme/orders#g'
go mod tidy && golangci-lint run ./... && go test -race ./...
```

Regenerate `gen/` with `buf generate` after a rename (generated code embeds its import path); never sed generated files. `assets/proto-templates/` holds the Buf config; `assets/observability/` runs a local Collector + Jaeger (`OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317`).

## Output

- Use the user's language. Lead with the root cause or the recommendation.
- Length:
  - quick question: ≤150 words
  - design: ≤400 words plus code
  - review: ≤500 words, findings by severity in ~3 lines each (problem → impact → fix), minor findings grouped on one line
- Include a section only if it changes the next step. Offer extras (manifests, dashboards, tests) in one line.
- Write full files to the repo; in chat show ≤150 lines of the non-obvious parts and list the files.
- Code must compile on the repo's go line, handle every error, set every timeout and release every resource.
