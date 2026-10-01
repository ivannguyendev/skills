# Resilience and load

How a Go service behaves when traffic exceeds capacity or a dependency slows
down: timeouts, load shedding, retries, circuit breakers, bulkheads, rate
limits and capacity sizing. The shared limiter lives in `internal/resilience`,
and the timeout rules are enforced in `cmd/server/config.go`.

## Contents

1. [Failure model](#failure-model)
2. [Timeout hierarchy](#timeout-hierarchy)
3. [Load shedding](#load-shedding)
4. [Retries with jitter and a budget](#retries-with-jitter-and-a-budget)
5. [Circuit breakers](#circuit-breakers)
6. [Bulkheads](#bulkheads)
7. [Rate limiting](#rate-limiting)
8. [Backpressure in asynchronous paths](#backpressure-in-asynchronous-paths)
9. [Capacity sizing with Little's law](#capacity-sizing-with-littles-law)
10. [Fail open or fail closed](#fail-open-or-fail-closed)
11. [Load and soak testing](#load-and-soak-testing)

## Failure model

Most high-load incidents come from five patterns. Each mechanism below targets
one or more of them.

| Pattern | What happens | Defense |
|---|---|---|
| Overload | Requests queue, latency rises, everything times out together | Load shedding with a bounded queue wait |
| Slow dependency | Goroutines and connections pile up waiting on it | Timeouts, bulkheads, circuit breaker |
| Dependency outage | Every request pays the full timeout before failing | Circuit breaker, degraded responses |
| Retry storm | Clients and layers retry together and multiply load several times over | Retry budgets, jitter, retrying at one layer only |
| Thundering herd | Cache expiry or a restart sends everyone to the database at once | singleflight, TTL jitter, warm-up |

## Timeout hierarchy

Each layer's timeout must be shorter than the layer above it. Otherwise the
outer layer gives up first, and the inner work keeps running for a client that
has already left.

```text
load balancer idle (60s) < HTTP IdleTimeout (90s)
HTTP WriteTimeout (10s)  > request Deadline (5s) > downstream call timeout (≤ 2s) > queue wait (25ms)
gRPC caller deadline     > this service's downstream call timeouts (derived from the incoming ctx)
SHUTDOWN_BUDGET (28s)    ≥ drain + max(HTTP, gRPC shutdown) + admin + telemetry flush
terminationGracePeriod (30s) > SHUTDOWN_BUDGET
```

`config.validate` in `cmd/server/config.go` rejects violations at startup. A
misconfiguration fails the deploy instead of surfacing as connection resets
under load. Deadlines propagate automatically when every call uses the request
context. `context.WithTimeout(ctx, d)` never extends a deadline that is already
shorter.

## Load shedding

`resilience.Limiter` caps concurrent work for the whole process. HTTP
(`httpserver.LoadShed`) and gRPC (`grpcserver.LoadShedUnary`) share one
instance, so neither transport can starve the other.

- **Reject rather than queue.** A request that can't get a slot within
  `QUEUE_WAIT` (25ms) gets **503 + `Retry-After`** or gRPC **`UNAVAILABLE`**.
  Both are retryable, so the load balancer or client moves to a replica with
  spare capacity. Unbounded queueing grows memory and latency until every
  request in the queue times out at once, with no useful work done.
- **Shed early.** The limiter runs before the body is read or any work starts,
  so a rejection costs microseconds and no allocations on the fast path.
- **Exempt health checks and long-lived streams.** Shedding probes marks an
  overloaded pod unready and pushes its traffic onto equally busy pods, which
  turns overload into a cascading outage. A stream would hold a slot for minutes;
  bound streams by count instead.
- **Size it:** `MAX_INFLIGHT` ≈ peak RPS × p99 latency × 2–3. See
  [capacity sizing](#capacity-sizing-with-littles-law).
- **Watch it:** export `InFlight()` as a gauge and `Rejected()` as a counter. A
  rising rejection rate means scale out, or lower latency first.

A fixed limit has to be tuned. When latency varies a lot, an adaptive limit
(AIMD) finds the capacity itself. It grows the limit while latency stays under
target and cuts it when latency exceeds it. Use it in place of the fixed
`resilience.Limiter` in the shedding middleware: `TryAcquire` to admit, and
`Release` with the measured latency.

```go
// Package adaptive is an AIMD concurrency limiter: additive increase while
// latency is healthy, multiplicative decrease (at most once per cooldown)
// when it is not.
package adaptive

import (
	"sync"
	"time"
)

// Limiter admits up to Current() requests at once and adapts that number.
type Limiter struct {
	mu           sync.Mutex
	limit        float64
	inFlight     int
	min, max     float64
	target       time.Duration // latency considered healthy
	cooldown     time.Duration // minimum spacing between decreases
	lastDecrease time.Time
}

// New starts at initial and stays within [minLimit, maxLimit] (minLimit ≥ 1).
func New(initial, minLimit, maxLimit int, target time.Duration) *Limiter {
	minLimit = max(minLimit, 1)
	return &Limiter{
		limit: float64(max(initial, minLimit)), min: float64(minLimit), max: float64(maxLimit),
		target: target, cooldown: target,
	}
}

// TryAcquire takes a slot if one is free; the caller sheds the request otherwise.
func (l *Limiter) TryAcquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight >= int(l.limit) {
		return false
	}
	l.inFlight++
	return true
}

// Release returns the slot and feeds the request's outcome back. Decreases
// are spaced by cooldown: a burst of slow completions caused by one stall must
// not collapse the limit to its minimum.
func (l *Limiter) Release(latency time.Duration, overloaded bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inFlight--
	now := time.Now()
	if overloaded || latency > l.target {
		if now.Sub(l.lastDecrease) >= l.cooldown {
			l.limit = max(l.min, l.limit*0.9)
			l.lastDecrease = now
		}
		return
	}
	l.limit = min(l.max, l.limit+1/l.limit) // grows by about 1 per window of requests
}

// Current returns the limit in force (export it as a gauge).
func (l *Limiter) Current() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return int(l.limit)
}
```

## Retries with jitter and a budget

Retry only when all of the following hold:
- the operation is **idempotent**, or carries an idempotency key;
- the error is **transient**: `UNAVAILABLE`, HTTP 502/503/504, connection
  reset;
- there is **deadline left** for another attempt to finish.

Retry at **one layer** only, usually the client closest to the dependency.
Retries at three layers of a call chain multiply load by 27.

```go
// Package retry runs an operation with capped full-jitter backoff, bounded by
// the caller's deadline and a shared retry budget.
package retry

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"
)

// Budget is a token bucket shared by all callers of one dependency (the same
// idea as gRPC's retryThrottling). Each first attempt earns ratio tokens and
// each retry spends one, so retries stay below ratio × calls over time. The
// cap means a long healthy period cannot bank enough tokens to retry every
// call when an outage starts.
type Budget struct {
	mu            sync.Mutex
	tokens, limit float64
	ratio         float64
}

// NewBudget allows retries at ratio (e.g. 0.1 = one retry per ten calls) with
// at most limit retries banked.
func NewBudget(ratio, limit float64) *Budget {
	return &Budget{tokens: limit, limit: limit, ratio: ratio}
}

func (b *Budget) earn() {
	b.mu.Lock()
	b.tokens = min(b.limit, b.tokens+b.ratio)
	b.mu.Unlock()
}

func (b *Budget) spend() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Do calls op up to attempts times (at least once). retryable decides which
// errors are worth another try. Backoff is full jitter in [0, min(base·2^n,
// maxBackoff)), and a retry is skipped when the caller's deadline would expire
// first or the budget is exhausted.
func Do(ctx context.Context, b *Budget, attempts int, base, maxBackoff time.Duration,
	retryable func(error) bool, op func(context.Context) error,
) error {
	b.earn()
	var err error
	for attempt := range max(attempts, 1) {
		if err = op(ctx); err == nil || !retryable(err) || attempt == attempts-1 {
			return err
		}
		ceiling := min(base<<min(attempt, 30), maxBackoff) // shift capped: no overflow
		if ceiling <= 0 {
			return err
		}
		sleep := time.Duration(rand.Int64N(int64(ceiling)))
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < sleep {
			return err // no time left for another attempt to finish
		}
		if !b.spend() {
			return err // budget exhausted: fail fast instead of piling on
		}
		t := time.NewTimer(sleep)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	return err
}
```

For gRPC, prefer the built-in machinery over hand-rolled loops. Use a
`retryPolicy` per method in the service config, plus `retryThrottling`
(`maxTokens`, `tokenRatio`), which is gRPC's own retry budget. See
`internal/grpcclient/grpc_client.go`.

## Circuit breakers

A breaker stops calling a dependency that keeps failing. Requests then fail in
microseconds instead of each waiting out a timeout, and the dependency gets
room to recover.

- **One breaker per dependency** (per downstream service and per database
  cluster), never one global breaker.
- **Only infrastructure failures count:** timeouts, `UNAVAILABLE`, 5xx. Domain
  errors (`NotFound`, validation) mean the dependency is healthy.
- **Settings:**
  - trip on a failure *ratio* over a minimum volume, not on N consecutive
    errors;
  - stay open for 5–30s;
  - allow a few probe requests while half-open.
- **When open:** return a degraded answer if one exists (cached value, default,
  partial response), otherwise `UNAVAILABLE` / 503.

```go
// Package breaker guards one dependency with sony/gobreaker/v2.
package breaker

import (
	"context"
	"errors"
	"time"

	"github.com/sony/gobreaker/v2"
)

// ErrUnavailable is returned while the breaker is open; map it to 503 or UNAVAILABLE.
var ErrUnavailable = errors.New("dependency unavailable")

// New trips when at least 20 requests in the window fail at a rate of 50% or more.
// isFailure must return false for domain errors such as "not found".
func New[T any](name string, isFailure func(error) bool) *gobreaker.CircuitBreaker[T] {
	return gobreaker.NewCircuitBreaker[T](gobreaker.Settings{
		Name:        name,
		MaxRequests: 3,                // probes allowed while half-open
		Interval:    10 * time.Second, // window for counting failures while closed
		Timeout:     15 * time.Second, // how long to stay open
		ReadyToTrip: func(c gobreaker.Counts) bool {
			return c.Requests >= 20 && float64(c.TotalFailures)/float64(c.Requests) >= 0.5
		},
		IsSuccessful: func(err error) bool { return err == nil || !isFailure(err) },
	})
}

// Call runs fn through cb and translates "breaker open" into ErrUnavailable.
func Call[T any](ctx context.Context, cb *gobreaker.CircuitBreaker[T], fn func(context.Context) (T, error)) (T, error) {
	v, err := cb.Execute(func() (T, error) { return fn(ctx) })
	if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
		return v, ErrUnavailable
	}
	return v, err
}
```

## Bulkheads

Isolate dependencies so that one slow dependency cannot consume every
goroutine and connection:
- one connection pool per database, with a bounded size and an acquire timeout;
- `MaxConnsPerHost` per outbound HTTP client (`references/architecture-and-http.md`).
  It caps sockets but *waits* for a free connection until the context ends,
  so on its own it queues and does not fail fast;
- a `resilience.Limiter` with a short queue wait in front of each slow
  dependency. This is what turns "full" into an immediate failure.

When the bulkhead rejects, the request fails fast instead of joining a queue
that drags down unrelated endpoints.

## Rate limiting

Rate limiting and load shedding protect different things:
- **Load shedding** protects *this process* from total overload.
- **Rate limiting** protects *fairness*: one tenant, API key or IP cannot use
  more than its share.

Rules:
- **Key by authenticated principal.** Fall back to client IP only when the IP
  comes from trusted proxy headers; chi's `ClientIPFromXFFTrustedProxies`
  handles this. A raw `X-Forwarded-For` header is attacker-controlled.
- **Bound the key store.** A `map[string]*rate.Limiter` keyed by IP grows
  forever. Keep per-key limiters in an LRU cache with a size cap and TTL.
- **Local vs distributed:**
  - A local token bucket (`golang.org/x/time/rate`) per instance is cheap and
    good enough when you can divide the limit by the replica count.
  - A distributed limiter (GCRA in Redis or Valkey) gives exact global limits
    but adds a network hop. Make it fail open to the local limiter when the
    store is down.
- **Answer 429** with `Retry-After` and `RateLimit-*` headers on HTTP, and
  `RESOURCE_EXHAUSTED` on gRPC.

## Backpressure in asynchronous paths

- **Consumers** fetch the next batch only after processing the current one, and
  set a max fetch size and max unacknowledged messages. That way a slow handler
  slows consumption instead of filling memory.
- **Internal queues** are bounded channels. Producers block with a timeout or
  shed; they never append to an unbounded slice.
- **Fan-out** uses `errgroup.SetLimit`, never one goroutine per item. See
  `references/concurrency-patterns.md`.
- **Producers to a broker** have explicit buffer limits, and surface errors
  when the broker is down instead of buffering without limit.

## Capacity sizing with Little's law

L = λ × W: in-flight work equals arrival rate × time in system. A worked
example for one instance at 2,000 RPS with p99 50 ms:

| Resource | Calculation | Setting |
|---|---|---|
| API in-flight | 2,000 × 0.05 s = 100 | `MAX_INFLIGHT=256` (2–3× headroom for bursts) |
| DB connections | 30% cache miss → 600 qps × 3 ms = 1.8 busy; writes 200 × 8 ms = 1.6 | pool max 16. Check: replicas × pool ≤ DB `max_connections` minus a reserve; beyond that use a pooler (PgBouncer) |
| Cache connections | 1,400 ops/s × 0.5 ms ≈ 1 busy | pool 32 is plenty; set it explicitly |
| Outbound HTTP | 300 RPS × 80 ms = 24 | `MaxConnsPerHost=64` |
| Consumer parallelism | target throughput ÷ per-message rate | partitions or consumers ≥ that number |

Re-derive these from measured p99 after every significant latency change.
Oversized pools hide problems and overload databases; undersized pools add
queueing latency.

## Fail open or fail closed

| Component down | Behaviour | Why |
|---|---|---|
| Authentication / authorization | **Fail closed** (401/403/503) | Never trade security for availability |
| Distributed rate limiter | Fail open to the local limiter | Losing exact limits is better than rejecting all traffic |
| Cache | Fail open to the source, behind singleflight and a DB bulkhead | Otherwise a cache outage becomes a database outage |
| Feature flags | Last known value, then the safe default | Flags must not become a single point of failure |
| Telemetry export | Drop and count | Observability must never block requests |

## Load and soak testing

```sh
# HTTP: constant arrival rate (open model) shows real queueing behaviour
echo "GET http://localhost:8080/v1/orders/42" | vegeta attack -rate=2000/s -duration=2m | vegeta report

# gRPC
ghz --insecure --call acme.orders.v1.OrderService/GetOrder -d '{"id":"…"}' \
    --rps 2000 --duration 2m localhost:50051
```

- **Use an open model (fixed arrival rate),** not a fixed number of looping
  clients. Looping clients slow down when the server does, which hides the
  queueing collapse you're trying to find.
- **Watch during the run:** p50/p99/p999 latency, error and shed rate,
  goroutine count, live heap, GC CPU, DB pool wait time, CPU throttling.
- **Step the rate up** to find the knee where p99 bends, and run in production
  at no more than about 70% of it.
- **Soak test** at production rate for 30–60 minutes. Heap and goroutines must
  plateau; anything that keeps rising is a leak
  (`references/performance-and-memory.md`).
