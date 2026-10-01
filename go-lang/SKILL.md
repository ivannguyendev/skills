---
name: go-lang
description: "Build, review, tune and debug enterprise Go servers: high-load, low-latency, memory-efficient, leak-free REST (net/http, chi) and gRPC services, team code style and package design, architecture (monolith vs services, hexagonal), framework trade-offs (chi, gin, echo, fiber, connect-go), concurrency, load shedding, retries, circuit breakers, pprof, GOMEMLIMIT, Protobuf/Buf and OpenTelemetry. Load it first, before reading or editing code, whenever the work involves designing or reviewing a Go service; goroutine or memory leaks, races, deadlocks; latency, allocation or GC tuning; server timeouts, graceful shutdown, health checks or mTLS; gRPC, .proto files or tracing; or replacing deprecated APIs like grpc.Dial or the Jaeger exporter. Use it even if the user only says 'Go service', 'API server' or 'microservice' or pastes Go code, and for Vietnamese requests like 'viết server Go chịu tải cao', 'bị leak goroutine', 'tối ưu hiệu năng Go', 'chuẩn code Go cho team'. Not for other languages or small CLIs."
metadata:
  version: "2.0.0"
  date-added: "2026-10-01"
  risk: "safe"
  source: "self"
  tested-with: "go1.27.1 and go1.26.8 (module floor go 1.26.0), grpc-go v1.83.2, otel-go v1.46.0, otelgrpc/otelhttp v0.71.0, chi v5.3.2, golangci-lint v2.14.0, buf 1.73.0"
---

# Go enterprise servers

This skill is for Go servers that have to stay fast and flat under real load,
and for codebases that many engineers change every day. It ships a verified
skeleton, `assets/service-skeleton`, with:
- HTTP and gRPC servers;
- a private admin port;
- shared load shedding;
- ordered shutdown;
- leak tests and allocation budgets;
- the team lint policy.

Adapt the skeleton rather than writing from memory: Go APIs changed a lot in
2023–2026, and code written from memory tends to call removed functions.

## Scope

Covers:
- Go language practice and team conventions
- architecture and package layout
- HTTP and gRPC servers and clients
- concurrency
- performance, memory and leaks
- resilience under load
- Protobuf/Buf
- OpenTelemetry tracing

Out of scope:
- database, cache and broker client code (apply the same rules: bounded pools,
  deadlines, closed resources)
- Kubernetes manifests
- other languages

## Establish the baseline first

1. **The go line and the dependencies.** Read `go.mod` and run
   `go list -m all | grep -E 'grpc|otel|protobuf|chi'`. Language semantics follow
   the go line, so write code that line can compile.
2. **Vulnerabilities.** Run `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`.
   The newest release is not always patched: when this skill was verified,
   grpc-go v1.84.0 was flagged and v1.83.2 was the patched line.
3. **Load profile and SLO:** peak RPS per instance, p99 target, payload sizes,
   memory and CPU limits. If nobody knows them, assume numbers, state the
   assumption, and size for it ([Little's law](references/resilience-and-load.md)).
4. **Where it runs:** load balancer idle timeout, mesh or not (it decides who
   does mTLS and retries), termination grace period.
5. **House conventions.** Follow the project on style. Follow this skill when
   the project relies on a removed API or an unbounded resource.

| Go | Use freely from here (full map in `references/go-style-and-idioms.md`) |
|---|---|
| 1.22 | Per-iteration loop variables, `for i := range n`, `ServeMux` patterns |
| 1.23 | Iterators; collectable unstopped timers |
| 1.24 | `tool` directive, `b.Loop()`, `t.Context()`, `os.Root` |
| 1.25 | `wg.Go`, `testing/synctest`, container-aware GOMAXPROCS, FlightRecorder |
| 1.26 (floor) | `errors.AsType`, `new(expr)`, Green Tea GC |
| 1.27 | Generic methods, `encoding/json/v2`, `goroutineleak` profile |

## Decisions

**Architecture** (`references/architecture-and-http.md`)

| Situation | Choose |
|---|---|
| One or a few teams, one release cadence | **Modular monolith**, hexagonal per module: the domain defines interfaces, adapters implement them, one wiring point |
| A module needs its own scaling, release cadence, data store or team | Extract it into a service that owns its data |
| Anything else (CQRS, event sourcing, DI framework, generic repository) | Only when a concrete requirement forces it |

**HTTP stack:**
- **Default:** chi on net/http, which keeps the whole net/http ecosystem.
- **Plain ServeMux:** fine for a few routes.
- **gin / echo:** acceptable when the team knows them. Mind reflection-based
  binding and context reuse.
- **fiber / fasthttp:** only when raw throughput beats ecosystem fit. It is not
  net/http, has no HTTP/2, and reuses values across requests.
- **connect-go / grpc-gateway:** when one proto contract must serve both gRPC
  and JSON.

**Concurrency** (`references/concurrency-patterns.md`)

| Need | Use |
|---|---|
| N tasks, stop on first error | `errgroup.WithContext` + `SetLimit` |
| Long-lived consumers of a stream | Worker pool with ctx-aware sends |
| Shared state | `sync.Mutex`; `atomic.Pointer` for read-mostly snapshots |
| Many callers want one key | `singleflight` (`DoChan` + recover) |
| Process-wide overload control | `resilience.Limiter` (skeleton) |

**Serialization:**
- **Public JSON:** `encoding/json` with pooled buffers (`WriteJSON`).
- **`encoding/json/v2`:** once the go line reaches 1.27.
- **Internal traffic:** Protobuf.
- **Faster JSON libraries:** only after a profile shows JSON dominating.

## Non-negotiables for high load

Every rule below prevents a specific production failure. Each comes with the
reason, so you can explain it in review.

**Lifecycle**
- Every goroutine has an owner, an exit path and a bound. Handlers never call
  `go f(r.Context())`, because the context dies when the handler returns.
- Constructors never start goroutines. Components expose `Run(ctx)` or
  `ServeListener(ctx, lis)`, so `main` can sequence startup and shutdown.
- Every channel send or receive also selects on `ctx.Done()`. Every `cancel` is
  deferred. Every body, rows, stream and ticker is closed on every path.

**Bounds**
- Every queue, cache, map keyed by request data, buffer, request body, page
  size and retry count has a deliberate limit. An unbounded map used as a cache
  is the most common slow leak.
- Copy what you keep out of large buffers (`bytes.Clone`); a sub-slice pins the
  whole array.

**Timeouts.** Each inner layer is shorter than the layer above it:
- LB idle < HTTP `IdleTimeout`;
- `WriteTimeout` > request deadline > downstream call timeout > queue wait;
- shutdown budget < termination grace period.

The skeleton's `config.validate` rejects violations at boot. `http.Server{}`
and `http.DefaultClient` have no timeouts and must never reach production.

**Overload**
- One process-wide `resilience.Limiter` sheds load for HTTP (503 +
  `Retry-After`) and gRPC (`UNAVAILABLE`) after a short queue wait. Queueing
  instead grows memory and latency until everything times out together.
- Health checks and long-lived streams are exempt, so overload never becomes an
  unhealthy pod and a cascade.
- Retry only idempotent operations on transient errors, with full jitter, at
  one layer, within a budget (`retryThrottling` for gRPC).
- One circuit breaker per dependency. Domain errors never trip it.

**Memory and latency**
- `GOMEMLIMIT` ≈ 90% of the container limit. Leave `GOGC` at 100. Set CPU
  requests, and for latency-sensitive services leave out CPU limits or set
  them generously: CFS throttling shows up as p99 spikes. Remove
  `automaxprocs` and heap ballast.
- Measure before optimizing: profile under load, fix the top item, then pin it
  with an allocation budget (`*_alloc_test.go`, `!race`) and a benchmark.
- `sync.Pool`:
  - pool pointers;
  - Reset on Get;
  - keep no reference after Put;
  - drop oversized objects.

**Errors and edges**
- Domain code returns neutral error kinds (`internal/apperr`); one boundary maps
  them to HTTP and gRPC codes. Internal error text never reaches clients.
- Translate downstream status codes; never forward them. A dependency's
  `InvalidArgument` is your bug.

**Observability**
- Span names and metric labels are low-cardinality route patterns (`RouteTag`
  for chi), never raw paths or IDs.
- Use JSON `slog` through the `*Context` methods so `trace_id` reaches the log.
  By default, log only errors and slow requests.

**Security**
- pprof and probes live on a private admin port (`internal/admin`), never on
  `http.DefaultServeMux`.
- mTLS is all-or-nothing: a partial TLS configuration fails startup.
- Trusted-proxy client IPs only: chi's `RealIP` is deprecated because it can be
  spoofed.

## Team code style

Summary; details and examples are in `references/go-style-and-idioms.md`.
- **Packages:** organized by domain, small APIs, `internal/` by default. No
  `util`/`common`/`models`, no stutter (`orders.Service`), no package-level
  mutable state or I/O in `init`.
- **Interfaces** are defined at the consumer, have one to three methods, and
  exist only once a second implementation or a fake does. Constructors take
  dependencies plus a `Config` struct with safe defaults, and return concrete
  types.
- **Errors:** wrap with what *this* function was doing (`%w`), handle once
  (log *or* return), use `errors.AsType`, and panic only for programmer errors.
- **Naming:** `ID`/`URL`/`HTTP` casing, no `Get` prefixes, `ErrX`/`XError`,
  short consistent receivers, snake_case file names.
- **Docs:** every export has a doc comment that states its contract
  (concurrency safety, ownership, zero value). Comments explain *why*.
- **Tests:**
  - table-driven, fakes over mocks;
  - real stack through `httptest` / `bufconn`;
  - `synctest` for time;
  - `goleak` in every package with goroutines;
  - `-race -shuffle=on` in CI.
- **Lint policy:** `assets/service-skeleton/.golangci.yaml` (golangci-lint v2;
  the skeleton passes with zero issues). Keep it quiet. Every `//nolint` names
  the linter and gives a reason.

## Workflows

**A. New service from the skeleton**
1. Copy and rename it ([Using the skeleton](#using-the-skeleton)).
2. Write down the load profile and set `MAX_INFLIGHT`, the timeouts and the
   pool sizes from it.
3. Add a domain package with its consumer-side interfaces.
4. Add adapters, and wire them in `newServers` (`cmd/server/main.go`).
5. Mount REST routes in the `NewHandler` callback. Register gRPC services on the
   `grpcserver.Server`, and give streaming services `Draining()`.
6. Tests: unit tests with fakes, then `httptest` / `bufconn` through the real
   chain, then a leak test under load modeled on
   `internal/httpserver/leak_test.go`.
7. Run `golangci-lint`, `go test -race`, `govulncheck`.

**B. Performance tuning** (`references/performance-and-memory.md`)
1. Set the target.
2. Load-test with an open model.
3. Profile CPU, allocs, mutex and trace from the admin port.
4. Fix the top item.
5. Run benchstat, then pin an allocation budget.
6. Consider PGO (`default.pgo`) once the code is stable.

**C. Memory or goroutine leak hunt**
1. Graph goroutines and live heap.
2. Group a goroutine dump with `?debug=1`.
3. Diff heap profiles with `-base`.
4. Match the cause against the leak-class table.
5. Reproduce in a test with `goleak` or a leak test, then fix it structurally.

**D. High-load readiness review.** Go through `references/production-checklist.md`
and `references/resilience-and-load.md`:
- timeouts, bounds, shedding, retries, breakers;
- pool sizing against Little's law;
- GOMEMLIMIT, CPU limits;
- shutdown budget.

**E. Code review.** Use the checklist in `references/go-style-and-idioms.md`.
State each finding as the concrete failure it causes.

**F. Add tracing.** Use `internal/telemetry` (SDK, propagator, flush last) and
the otelgrpc/otelhttp stats handlers. Name spans after routes, correlate logs
through `*Context`. If traces are missing, see `references/tracing-backends.md`.

**G. Change a proto API.**
1. Make additive changes.
2. Reserve every removed number and name.
3. Run `buf breaking` before merging.
4. Add `v2` alongside `v1` for a redesign.

See `references/protobuf-and-buf.md`.

## Corrections to outdated advice

| If you see | Do instead | Why |
|---|---|---|
| `http.ListenAndServe` / `&http.Server{}` without timeouts | `internal/httpserver` (safe defaults) | Slowloris and idle connections pin goroutines and file descriptors |
| `http.DefaultClient`, default `Transport` | Tuned client per dependency (timeout, `MaxIdleConnsPerHost`, `MaxConnsPerHost`) | No timeout; 2 idle connections per host means handshakes on every burst |
| `io.ReadAll(r.Body)` with no limit | `BodyLimit` + `DecodeJSON` (413) | Attacker-chosen memory use |
| `import _ "net/http/pprof"` with the default mux served publicly | pprof on the private admin mux | Exposes profiles and heap contents |
| `http.TimeoutHandler` around handlers | `Deadline` middleware (ctx deadline) | Buffers whole responses; breaks streaming |
| chi `middleware.RealIP` | `ClientIPFromXFFTrustedProxies` | Deprecated; IP spoofing |
| `grpc.Dial`, `DialContext`, `WithBlock` | `grpc.NewClient`; let the first RPC fail | Deprecated; deploy-order coupling, crash loops |
| otelgrpc interceptors | `grpc.StatsHandler(otelgrpc.NewServerHandler())` | Removed from otelgrpc |
| `otel/exporters/jaeger`, ports 6831 / 14268 | OTLP (4317 / 4318) to a Collector | Exporter removed; Jaeger v1 is EOL |
| Raw path as span name or metric label | Route pattern (`RouteTag`) | Unbounded cardinality is a memory leak in the telemetry pipeline |
| Worker `out <- v` with no `select` | `select { case out <- v: case <-ctx.Done(): }` | Goroutine leak when the reader stops |
| `map[string]*rate.Limiter` per IP, unbounded caches | Size cap + TTL | Grows with every distinct key |
| Unbounded queueing under overload | Limiter with a bounded queue wait → 503 / `UNAVAILABLE` | Latency and memory explode, then everything times out |
| Retries on `DEADLINE_EXCEEDED`, or at every layer | `UNAVAILABLE` only, one layer, full jitter, budget | Retries share one deadline; layered retries multiply load |
| Heap ballast, `automaxprocs` | `GOMEMLIMIT`; nothing for GOMAXPROCS (1.25+) | Obsolete runtime workarounds |
| `sync.Pool` of `[]byte` values | Pool of `*bytes.Buffer` with a cap guard | Value pools allocate; huge buffers stay pinned |
| `wg.Add(1); go …; defer wg.Done()` | `wg.Go(f)` | Misplaced `Add` races with `Wait` |
| `var t *T; errors.As(err, &t)` | `errors.AsType[*T](err)` | One expression, type-checked (1.26) |
| `for i := 0; i < b.N; i++` | `for b.Loop()` | Accurate benchmarks (1.24) |
| `tools.go` blank imports | `go get -tool` + `go tool` | Versions pinned in go.mod |
| `os.Exit` / `log.Fatal` with defers pending | `os.Exit(realMain())` | Deferred cleanup and telemetry flush never run |
| `protoc-gen-validate`, `github.com/golang/protobuf` | protovalidate, `google.golang.org/protobuf` | Archived / legacy |
| "Latest version is safest" | The newest *patched* version per `govulncheck` | Releases ship vulnerabilities too |

## Reference map

Read only what the task needs. Every long file has a table of contents.

| File | Read when |
|---|---|
| `references/go-style-and-idioms.md` | Team conventions: naming, packages, interfaces, constructors, errors, context, logging, generics, docs, tests, lint, review checklist, version features |
| `references/architecture-and-http.md` | Choosing monolith vs services, layout and dependency rule, wiring, HTTP framework choice, server hardening, middleware order, JSON, streaming, outbound clients |
| `references/performance-and-memory.md` | Profiling, allocation rules, `sync.Pool`, GC/GOMEMLIMIT tuning, PGO, leak classes, finding leaks, proving it in tests |
| `references/resilience-and-load.md` | Timeout hierarchy, load shedding, retries and budgets, breakers, bulkheads, rate limits, backpressure, capacity sizing, load testing |
| `references/concurrency-patterns.md` | errgroup, worker pools, pipelines, semaphores, singleflight, sharded maps, synctest, background workers, detached work |
| `references/grpc-server.md` | Server options, interceptor order, services, streaming, auth, error model, health/probes, shutdown, TLS |
| `references/grpc-client-and-testing.md` | NewClient, service config, retries, calling dependencies, bufconn and interceptor tests |
| `references/protobuf-and-buf.md` | `.proto` design, Buf, breaking changes, codegen, protovalidate |
| `references/opentelemetry-go.md` | SDK setup, resources, otelgrpc/otelhttp, manual spans, propagation, log correlation, sampling |
| `references/tracing-backends.md` | Collector, Jaeger v2, Tempo, TraceQL, "no traces" troubleshooting |
| `references/production-checklist.md` | Pre-ship and high-load readiness review |

## Using the skeleton

`assets/service-skeleton/` is a Go module. It builds and lints clean, and its
tests pass under `-race`, including leak tests and allocation budgets.

```text
cmd/server/            main → config (validated timeout hierarchy) → wiring (newServers) → lifecycle (ordered shutdown)
internal/httpserver/   hardened http.Server, chi chain (RouteTag, Recover, AccessLog, LoadShed, Deadline, BodyLimit),
                       WriteJSON / DecodeJSON, leak test, alloc budget
internal/grpcserver/   gRPC server: keepalive, health (Watch ends on drain), interceptors, load shedding, error mapping
internal/grpcclient/   NewClient factory: service config with retryThrottling, keepalive, downstream-error wrapping
internal/admin/        private port: /livez, /readyz, pprof
internal/resilience/   process-wide concurrency limiter (0 allocations)
internal/telemetry/    OTel SDK setup, slog trace handler
internal/tlsconfig/    mTLS with certificate reload
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

Generated protobuf code embeds its import path in descriptor bytes. After
renaming the module, regenerate `gen/` with `buf generate`; never rewrite
generated files with sed. `assets/proto-templates/` holds the Buf config and an
example API. For a local tracing stack, run
`docker compose -f <skill-dir>/assets/observability/docker-compose.yaml up -d`
and point `OTEL_EXPORTER_OTLP_ENDPOINT` at `http://localhost:4317`.
