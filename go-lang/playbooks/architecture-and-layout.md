# Architecture and layout

## Choosing a model

| Situation | Choose |
|---|---|
| One or a few teams, one release cadence | Modular monolith, hexagonal per module (default) |
| A module needs its own scaling, release cadence, SLA, store or team | Extract a service that owns its data |
| Spiky, short, stateless jobs | Functions |

- Avoid the *distributed monolith*: services that share tables or must deploy together.
- No CQRS, event sourcing, generic repositories, DTO layers or DI framework until a concrete requirement demands one.

## Choosing an HTTP stack

| Stack | Pick when | Watch out for |
|---|---|---|
| `net/http` `ServeMux` | Few routes | Middleware composed by hand |
| **chi v5** (default, skeleton) | Public REST on net/http: groups, sub-routers, `http.Handler` everywhere | Name spans with the route pattern (`RouteTag`); `RealIP` is spoofable |
| gin | Team already knows it | Reflection binding allocates; `*gin.Context` is pooled, `c.Copy()` before a goroutine |
| echo v5 | gin-like, `error`-returning handlers | v4 gets fixes only until 2026-12-31; OTel middleware pre-1.0 |
| fiber v3 (fasthttp) | Raw throughput on simple endpoints beats ecosystem fit | Not net/http: no HTTP/2, can't share a port with gRPC, otelhttp/pprof need adapters; `fiber.Ctx` values are reused across requests |
| connect-go | One handler serves gRPC, gRPC-Web and JSON from one proto | Different API from grpc-go |
| grpc-gateway | REST generated from proto annotations | Extra JSON↔proto hop |

The framework rarely limits throughput; hot-path allocation, DB round trips, lock contention and logging do. Prefer staying on `net/http`, where the tooling lives.

Serialization: public JSON with `encoding/json` and pooled buffers (`encoding/json/v2` from go 1.27); Protobuf internally; a faster JSON library only after a profile shows JSON dominating.

## Layout and dependency rule

```text
cmd/server/              main → config (validated) → wiring → lifecycle
internal/orders/         domain: entities, use cases, needed interfaces (stdlib only)
internal/orders/postgres adapter → orders.Repository
internal/orders/httpapi  handlers → use cases
internal/orders/grpcapi  generated server → use cases
internal/platform/...    httpserver, grpcserver, resilience, telemetry, admin (no domain imports)
proto/, gen/
```

- Dependencies point inward: the domain imports no transport and no storage.
- Interfaces sit at the consumer with 1–3 methods. Constructors return concrete types and take a `Config` with safe defaults.
- Modules call each other only through exported use cases, never through repositories or tables. This keeps a module extractable.
- Transactions live in adapters: one repository method owns a multi-aggregate change; `*sql.Tx` never enters the domain.
- Enforce the rule with `depguard` in CI, or `go list -deps ./internal/orders | grep -E 'grpc|net/http|pgx'`.
- No package-level mutable state and no I/O in `init`.

## Wiring `main`

Wire by hand in one function. Components expose a blocking `Serve(ctx, lis)` or `Run(ctx)`.

```go
func main() { os.Exit(realMain()) } // os.Exit/log.Fatal elsewhere would skip defers

func realMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	cfg, err := loadConfig() // rejects a bad timeout chain
	if err != nil {
		slog.Error("config", "err", err)
		return 2
	}
	app, cleanup, err := wire(cfg) // builds everything, starts nothing
	if err != nil {
		slog.Error("wire", "err", err)
		return 1
	}
	defer cleanup() // clients and pools close after the servers

	// Admin and workers outlive the drain (probes answer, relay publishes);
	// if either exits, defer stop() triggers shutdown.
	lateCtx, stopLate := context.WithCancel(context.WithoutCancel(ctx))
	late, lateCtx := errgroup.WithContext(lateCtx)
	late.Go(func() error { defer stop(); return app.admin.Serve(lateCtx, app.adminLis) })
	late.Go(func() error { defer stop(); return app.relay.Run(lateCtx) })

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return app.http.Serve(gctx, app.httpLis) })
	g.Go(func() error { return app.grpc.Serve(gctx, app.grpcLis) })
	err = g.Wait()
	app.recorder.Wait() // detached post-response work
	stopLate()
	err = errors.Join(err, late.Wait())
	if err != nil {
		slog.Error("stopped", "err", err)
		return 1
	}
	return 0
}
```

Bind every listener before reporting ready, so a port conflict fails boot instead of leaving the service half-up.

## Shutdown order

The budget (drain + server shutdown + flush) must be less than `terminationGracePeriodSeconds`. Each `Serve` runs steps 1–3 when its ctx is cancelled.

1. Set readiness off (a shared flag read by `/readyz`) and gRPC health to `NOT_SERVING`.
2. Wait `DrainDelay` (3–5s) while endpoints propagate.
3. Close `draining` so streams end, then run `Shutdown`/`GracefulStop` in parallel with timeouts, falling back to `Stop()`.
4. Stop workers, then close clients and DB pools.
5. Stop admin, then flush telemetry with a fresh 5s ctx.

A SIGTERM that arrives before `Serve` starts is still a clean exit (0).

## Errors at the edge

- Domain code returns neutral kinds (`ErrNotFound`, `ErrConflict`, `ErrInvalid`), wrapped with `%w` and saying what *this* function was doing.
- One boundary per transport maps kinds to codes and logs the cause once. Internal text never reaches clients.
- Never forward a dependency's status code: its `InvalidArgument` is your bug. Map it to `Internal` or `Unavailable`.
- Liveness checks only whether the process is wedged, never the DB; otherwise a DB outage restarts every pod. Readiness may check dependencies.
