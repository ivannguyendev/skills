# Resilience and load

## Timeout hierarchy

Each inner layer is shorter than the outer one; otherwise the work keeps running for a client that has already left. Validate the chain in config at boot.

```text
LB idle 60s < IdleTimeout 90s
WriteTimeout 10s > request deadline 5s > downstream ≤2s > queue wait ~25ms
gRPC: downstream deadlines derive from the incoming ctx
shutdown budget 28s < terminationGracePeriod 30s
```

## HTTP server and client

```go
srv := &http.Server{
	Handler:           h,
	ReadHeaderTimeout: 5 * time.Second, // Slowloris
	ReadTimeout:       10 * time.Second,
	WriteTimeout:      10 * time.Second, // > handler deadline
	IdleTimeout:       90 * time.Second, // > LB idle
	MaxHeaderBytes:    64 << 10,
	ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
}

// One per dependency, shared. maxConns ≈ RPS × p99 × 2.
func NewClient(timeout time.Duration, maxConns int) *http.Client {
	d := &net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Client{
		Timeout: timeout,
		Transport: otelhttp.NewTransport(&http.Transport{
			Proxy: http.ProxyFromEnvironment, DialContext: d.DialContext, ForceAttemptHTTP2: true,
			MaxIdleConns: maxConns, MaxIdleConnsPerHost: maxConns, // default 2 → handshake per burst
			MaxConnsPerHost: maxConns, IdleConnTimeout: 90 * time.Second,
			TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: timeout,
		}),
	}
}
```

- Per-request deadline via middleware, not `http.TimeoutHandler`, which buffers responses. Cap the body with `http.MaxBytesReader` and answer 413.
- `defer resp.Body.Close()` and read through `io.LimitReader`; an unclosed body leaks a connection and 2 goroutines.
- `otelhttp.Transport` doesn't forward `CloseIdleConnections`. Keep the base `*http.Transport` and call it at shutdown and in tests.

## Load shedding

One `Limiter` per process, shared by HTTP and gRPC, placed before the body is read. Exempt health checks; bound streams by count instead.

```go
var ErrOverloaded = errors.New("overloaded")

type Limiter struct {
	slots     chan struct{} // len = in-flight gauge
	queueWait time.Duration
	rejected  atomic.Int64
}

func NewLimiter(maxInFlight int, queueWait time.Duration) *Limiter {
	return &Limiter{slots: make(chan struct{}, max(maxInFlight, 1)), queueWait: queueWait}
}

func (l *Limiter) Acquire(ctx context.Context) error {
	select {
	case l.slots <- struct{}{}: // fast path, 0 allocs
		return nil
	default:
	}
	t := time.NewTimer(l.queueWait)
	defer t.Stop()
	select {
	case l.slots <- struct{}{}:
		return nil
	case <-t.C:
		l.rejected.Add(1)
		return ErrOverloaded
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *Limiter) Release() { <-l.slots }

func LoadShed(l *Limiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := l.Acquire(r.Context()); err != nil {
				if errors.Is(err, ErrOverloaded) { // else client gave up: not overload
					w.Header().Set("Retry-After", "1")
					http.Error(w, "overloaded", http.StatusServiceUnavailable)
				}
				return
			}
			defer l.Release()
			next.ServeHTTP(w, r)
		})
	}
}

func LoadShedUnary(l *Limiter) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if strings.HasPrefix(info.FullMethod, "/grpc.health.v1.Health/") {
			return h(ctx, req)
		}
		if err := l.Acquire(ctx); err != nil {
			if errors.Is(err, ErrOverloaded) {
				return nil, status.Error(codes.Unavailable, "overloaded")
			}
			return nil, status.FromContextError(err).Err()
		}
		defer l.Release()
		return h(ctx, req)
	}
}
```

A shed `UNAVAILABLE` is meant to be retried on another replica, and the client's `retryThrottling` bounds that. If latency is very variable, use an AIMD limit: +1/limit per healthy request, ×0.9 above target, at most once per cooldown.

## Sizing (Little's law)

In-flight = RPS × latency:
- 2,000 RPS × 50 ms = 100 → `MAX_INFLIGHT` ≈ 256.
- DB: 600 qps × 3 ms ≈ 2 busy → pool ~16. Check replicas × pool ≤ `max_connections` minus a reserve.
- Outbound: 300 RPS × 80 ms = 24 → `MaxConnsPerHost` 64.

Re-derive after every latency change.

## Circuit breaker (one per dependency)

```go
func NewBreaker[T any](name string, isInfraFailure func(error) bool) *gobreaker.CircuitBreaker[T] {
	return gobreaker.NewCircuitBreaker[T](gobreaker.Settings{ // sony/gobreaker/v2
		Name: name, MaxRequests: 3, // half-open probes
		Interval: 10 * time.Second, Timeout: 15 * time.Second, // count window, open time
		ReadyToTrip: func(c gobreaker.Counts) bool {
			return c.Requests >= 20 && float64(c.TotalFailures)/float64(c.Requests) >= 0.5
		},
		IsSuccessful: func(err error) bool { return err == nil || !isInfraFailure(err) },
	})
}
```

- Trip on a failure ratio over a minimum volume, not on N consecutive errors.
- Only timeouts, `UNAVAILABLE` and 5xx count as failures; `NotFound` or a validation error means the dependency is healthy.
- When open: serve a degraded answer (cache or default), else 503/`UNAVAILABLE`. Never retry an open breaker.

## Retries

- Retry only when the call is idempotent (or carries an idempotency key), the error is transient (`UNAVAILABLE`, 502/503/504, connection reset), and the deadline leaves room for another attempt.
- One layer only, closest to the dependency: 3 layers × 3 attempts = 27× load.
- Full jitter: `rand.Int64N(max(1, min(base<<min(n, 20), cap)))`. Skip the retry if the sleep would outlive the deadline.
- Budget ~10% of calls per dependency. For gRPC, use service-config `retryPolicy` + `retryThrottling`.

## Rate limits and bounded maps

- A rate limit buys fairness per tenant (429/`RESOURCE_EXHAUSTED`); it is not overload protection.
- Key by authenticated principal. Take the client IP only from trusted proxies; raw `X-Forwarded-For` is attacker-controlled.
- A per-key `map[string]*rate.Limiter` grows forever: use an LRU with a cap and TTL. The same goes for every request-keyed map.
- Internal queues are bounded channels; producers shed or block with a timeout.

## Fail open or closed

- Auth fails closed.
- A distributed rate limiter fails open to the local one.
- A cache fails open to the source behind singleflight and a DB bulkhead.
- Telemetry drops and counts; it never blocks.

## Load testing

- Use an open model (`vegeta -rate`, `ghz --rps`); looping clients hide queueing collapse.
- Find the knee where p99 bends and run at ≤70% of it.
- Soak for 30–60 min: heap and goroutine count must plateau.
