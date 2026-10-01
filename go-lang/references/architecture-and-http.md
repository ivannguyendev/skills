# Architecture and HTTP

How to structure a Go service so it scales with the team, which HTTP stack to
pick, and how to harden the HTTP edge. The running reference is
`assets/service-skeleton`: `cmd/server` plus `internal/httpserver`,
`internal/admin` and `internal/resilience`.

## Contents

1. [Choosing the architecture model](#choosing-the-architecture-model)
2. [Layout and the dependency rule](#layout-and-the-dependency-rule)
3. [Wiring without a DI framework](#wiring-without-a-di-framework)
4. [Choosing an HTTP stack](#choosing-an-http-stack)
5. [Hardening the HTTP server](#hardening-the-http-server)
6. [Middleware order](#middleware-order)
7. [Requests and responses](#requests-and-responses)
8. [Streaming responses](#streaming-responses)
9. [Outbound HTTP clients](#outbound-http-clients)

## Choosing the architecture model

| Model | Choose when | Costs |
|---|---|---|
| **Modular monolith** (default) | One team or a few, one release cadence, shared database is acceptable | Needs package discipline (below) to stop becoming a big ball of mud |
| **Microservices** | A context needs its own release cadence, SLA, scaling profile, data store or owning team | Network calls instead of function calls, distributed failure modes, eventual consistency, a platform team |
| Serverless / functions | Spiky, short, stateless jobs | Cold starts, no long-lived connections or in-process caches |

- **Start with a modular monolith whose modules could be extracted.** The usual
  failure is a *distributed monolith*: services that share a database or must
  deploy together. It has every cost of microservices and none of the benefits.
- **When to split a module out:** it needs independent scaling (e.g. CPU-heavy
  rendering next to latency-sensitive APIs), independent deploys, a different
  data store, or a separate team. Extraction is mechanical when the dependency
  rule below has been respected.
- **One service = one data store owner.** Other services go through its API or
  its events, never its tables.
- **Defaults to avoid until a requirement forces them:** CQRS, event sourcing,
  generic repositories, layers of DTO mapping, DI frameworks. Each adds moving
  parts that the team pays for on every change.

## Layout and the dependency rule

```text
cmd/server/                main → config → wiring → lifecycle (see the skeleton)
internal/orders/           a bounded context: entities, rules, use cases, and the
                           interfaces it needs (Repository, Publisher) — stdlib only
internal/orders/postgres/  adapter implementing orders.Repository
internal/orders/httpapi/   chi handlers calling the use cases
internal/orders/grpcapi/   generated-server implementation calling the use cases
internal/platform/...      cross-cutting: httpserver, grpcserver, telemetry, resilience
                           (in the skeleton these sit directly under internal/)
gen/, proto/               generated code and its sources
```

- **Dependencies point inwards:** adapters import the domain, and the domain
  imports neither transport nor storage. Platform packages never import a
  domain. One wiring point imports everything.
- **Interfaces live in the domain package** (the consumer); adapters satisfy
  them. That is hexagonal architecture expressed with Go's implicit interfaces,
  and it needs no framework.
- **Transactions belong to adapters.** A use case that must change two
  aggregates atomically calls one repository method that owns the transaction,
  rather than threading `*sql.Tx` through the domain.
- **Map errors once:** domain code returns neutral kinds (`internal/apperr`).
  The gRPC boundary (`internal/grpcserver/error_mapping.go`) and the HTTP
  boundary each translate them into status codes.
- **Between modules,** call the other module's exported use-case API, never its
  repository or tables. That one rule is what keeps a monolith extractable.

## Wiring without a DI framework

The skeleton wires everything in one function, `newServers` in
`cmd/server/main.go`. Constructors receive their dependencies as parameters,
and the lifecycle (`cmd/server/lifecycle.go`) starts and stops components in a
known order.

- **Why manual:**
  - The compiler checks the graph.
  - Startup order is plain code you can read.
  - Tests build the same graph with fakes.
  - There is no reflection at startup.
- **When to reach for a framework:** `google/wire` (compile-time code
  generation) or `uber-go/fx` (runtime lifecycle hooks) can pay off once the
  graph has dozens of components shared across several binaries. Even then, keep
  constructors framework-free so packages don't depend on the container.

## Choosing an HTTP stack

| Stack | Pick it when | Watch out for |
|---|---|---|
| `net/http` `ServeMux` (1.22+) | Few routes, no middleware ecosystem needed | Method and wildcard patterns only; you compose middleware by hand |
| **chi v5** (skeleton default) | Public REST on top of net/http: route groups, sub-routers, middleware, `http.Handler` everywhere | chi records the route pattern on a copy of the request, so add `RouteTag` (`internal/httpserver/route_tag.go`) for span names. `middleware.RealIP` is deprecated (spoofable); use `ClientIPFromXFFTrustedProxies` |
| gin v1 | The team already knows it; you want binding and validation built in | Reflection-based binding costs allocations. `*gin.Context` is pooled, so call `c.Copy()` before using it in a goroutine |
| echo v5 | Similar to gin, with an `error`-returning handler style | v4 gets fixes only until 2026-12-31. Its OTel middleware is pre-1.0 |
| fiber v3 (fasthttp) | Raw throughput on simple endpoints beats ecosystem fit | **Not net/http:** no HTTP/2 server, so it can't share a port with gRPC, and net/http middleware (otelhttp, pprof) needs adapters. `fiber.Ctx` values are **reused across requests**: copy anything you keep, or set `Immutable: true` |
| connect-go | One handler should serve gRPC, gRPC-Web and HTTP/JSON from the same proto | A different server and client API from grpc-go |
| grpc-gateway | REST generated from proto annotations, gRPC as the source of truth | An extra JSON↔proto hop and annotation upkeep |

Framework choice is rarely what limits throughput. The things that do limit it
are allocation on hot paths, database round trips, lock contention and
logging. Prefer the stack that keeps you on `net/http`, because that is where
the tooling lives.

## Hardening the HTTP server

`internal/httpserver/http_server.go` treats a zero value as "safe default",
never "unlimited". A bare `http.Server{}` has no timeouts at all.

| Setting | Skeleton default | Prevents |
|---|---|---|
| `ReadHeaderTimeout` | 5s | Slowloris: clients that trickle headers pin goroutines forever (`gosec` G112) |
| `ReadTimeout` | 10s | Slow request bodies |
| `WriteTimeout` | 10s | Stuck writers. Keep it above the per-request `Deadline` (5s); `config.validate` enforces this |
| `IdleTimeout` | 90s | Idle keep-alive connections piling up. Keep it above the load balancer's idle timeout, or the LB reuses connections the server already closed |
| `MaxHeaderBytes` | 64 KiB | Header floods |
| `BodyLimit` middleware | 1 MiB | Unbounded body buffering; `DecodeJSON` maps the error to 413 |
| `Deadline` middleware | 5s | Work continuing after the client stopped waiting. Use it instead of `http.TimeoutHandler`, which buffers the whole response and breaks streaming |

Shutdown is `Shutdown` with a budget, then `Close`. `Shutdown` does not wait
for hijacked connections (WebSockets), so their handlers must watch the
context themselves.

## Middleware order

From `internal/httpserver/handler.go`, outermost first:

1. **otelhttp:** spans and metrics for every request, including rejected ones.
2. **RouteTag:** names the span and `http.route` after the chi pattern
   (`GET /v1/orders/{id}`). Raw paths would create one series per ID, which is
   unbounded cardinality.
3. **AccessLog:** sees the final status, including 500s from Recover and 503s
   from LoadShed. By default it logs only 5xx and slow requests, with the route
   pattern.
4. **Recover:** a panic becomes a 500. If the response has already started, the
   connection is aborted instead, because a truncated 200 would look like
   success. `http.ErrAbortHandler` is re-raised, because net/http handles it on
   purpose.

API routes (`Routes.API`) then add:

5. **LoadShed:** the shared limiter rejects before any body is read. A client
   that gives up while queued gets 499, which is not counted as a server error.
6. **Deadline:** the per-request budget.
7. **BodyLimit:** caps memory per request.

Streaming routes (`Routes.Streams`) skip LoadShed and Deadline (next section).

Things to add per deployment, inside this chain:
- client-IP resolution with trusted proxies;
- authentication;
- per-principal rate limiting;
- CORS (only for browser clients);
- `http.CrossOriginProtection` (1.25) for cookie-authenticated forms.

Health and pprof never go through this chain; they live on the admin port
(`internal/admin`).

## Requests and responses

- **`DecodeJSON`** accepts exactly one JSON value and rejects unknown fields.
  It also rejects a wrong content type (415), an oversized body (413) and
  malformed JSON (400). Validate business rules in the domain, not with
  reflection-based tags on hot paths.
- **`WriteJSON`** encodes into a pooled buffer first, so an encoding failure
  becomes a clean 500 and `Content-Length` is known. The skeleton pins it at 3
  allocations (`respond_alloc_test.go`).
- **Errors for public APIs:** use RFC 9457 `application/problem+json` with a
  stable `type` URI per error kind, mapped from `apperr`. Never send
  `err.Error()` of internal failures.
- **Writes:**
  - Accept `Idempotency-Key` on POSTs that create things, store the key with the
    result, and replay it for retries.
  - Use `ETag` + `If-Match` (412 on mismatch) for optimistic concurrency on
    updates.
- **Pagination** uses opaque cursor tokens (keyset), never `OFFSET`. OFFSET gets
  slower with every page and skips or duplicates rows under concurrent writes.

## Streaming responses

Long-lived responses (Server-Sent Events, long polling, large downloads) are
different from API calls in three ways, and the skeleton handles each one:
- **The request deadline doesn't apply.** Register them through
  `Routes.Streams`, which skips the 5s `Deadline` and the shared load-shedding
  slot. A stream would hold that slot for minutes.
- **The server-wide `WriteTimeout` doesn't apply either.** Extend the deadline
  per write with `http.ResponseController`.
- **Shutdown must end them.** `http.Server.Shutdown` waits for handlers but never
  cancels them. `cmd/server` passes a `drain` channel that `serve` closes right
  before the servers stop; streams return on it.

```go
// Package events streams Server-Sent Events without disabling server timeouts.
package events

import (
	"fmt"
	"io"
	"net/http"
	"time"
)

// Stream writes one event per message until the client disconnects, the
// feed closes or the server drains (the drain channel from cmd/server).
func Stream(feed <-chan string, drain <-chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w) // works through wrappers that implement Unwrap
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		if rc.Flush() != nil { // send headers now: proxies time out idle streams otherwise
			return
		}

		keepalive := time.NewTicker(15 * time.Second)
		defer keepalive.Stop()
		for {
			var payload string
			select {
			case <-r.Context().Done():
				return
			case <-drain:
				return
			case <-keepalive.C:
				payload = ": keepalive\n\n" // comment line: keeps proxies and LBs from closing the connection
			case msg, ok := <-feed:
				if !ok {
					return
				}
				payload = fmt.Sprintf("data: %s\n\n", msg)
			}
			// A per-write deadline replaces the server-wide WriteTimeout for this stream.
			if rc.SetWriteDeadline(time.Now().Add(10*time.Second)) != nil {
				return
			}
			if _, err := io.WriteString(w, payload); err != nil || rc.Flush() != nil {
				return
			}
		}
	}
}
```

Mount it with `Routes{Streams: func(r chi.Router) { r.Get("/v1/events", events.Stream(feed, drain)) }}`.
Bound streams by connection count, for example `MaxConnsPerHost` at the load
balancer or a dedicated limiter that admits a fixed number of streams.

## Outbound HTTP clients

`http.DefaultClient` has no timeout, and the default transport keeps only two
idle connections per host. At high load that means unbounded waits and
constant TLS handshakes. Build one tuned client per dependency at startup and
share it.

```go
// Package httpclient builds tuned, instrumented clients for outbound calls.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// New returns a client for one dependency. maxConns ≈ peak RPS to that
// dependency × its p99 latency × 2 (Little's law with headroom).
func New(timeout time.Duration, maxConns int) *http.Client {
	dialer := &net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          maxConns,
		MaxIdleConnsPerHost:   maxConns, // the default of 2 forces a new connection per burst
		MaxConnsPerHost:       maxConns, // caps sockets; callers wait for one until ctx ends
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   3 * time.Second,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: time.Second,
	}
	return &http.Client{
		Transport: otelhttp.NewTransport(transport), // propagates traceparent
		Timeout:   timeout,                          // ceiling; the request ctx usually ends sooner
	}
}

// maxBody caps how much of a response is read into memory.
const maxBody = 1 << 20

// GetBody performs a GET and returns at most maxBody bytes. The body is always
// drained and closed, so the connection returns to the pool. An unclosed body
// leaks a connection and two goroutines.
func GetBody(ctx context.Context, c *http.Client, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		// *url.Error prints the full URL, query string included, which may
		// carry tokens. Keep only the cause and the host.
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err
		}
		return nil, fmt.Errorf("get %s: %w", req.URL.Host, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody)) // drain so the connection is reused
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get %s: status %d", req.URL.Host, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("get %s: response exceeds %d bytes", req.URL.Host, maxBody)
	}
	return body, nil
}
```

Retries, breakers and budgets for these calls are covered in
`references/resilience-and-load.md`.
