# Concurrency patterns

Goroutines, channels, sync primitives and context for service code. Every Go
block is a complete file that compiles with the skill's module floor,
`go 1.26.0`. `sync.WaitGroup.Go` and `testing/synctest` need 1.25 or later.

## Contents

1. [Rules that prevent most bugs](#rules-that-prevent-most-bugs)
2. [Choosing a primitive](#choosing-a-primitive)
3. [Bounded fan-out with errgroup](#bounded-fan-out-with-errgroup)
4. [Worker pool](#worker-pool)
5. [Pipelines and fan-in](#pipelines-and-fan-in)
6. [Admission control: semaphores and rate limits](#admission-control-semaphores-and-rate-limits)
7. [Coalescing duplicate work with singleflight](#coalescing-duplicate-work-with-singleflight)
8. [Shared maps](#shared-maps)
9. [Timed loops and deterministic tests with synctest](#timed-loops-and-deterministic-tests-with-synctest)
10. [Owned background workers](#owned-background-workers)
11. [Work that outlives the request](#work-that-outlives-the-request)
12. [Finding races, leaks and deadlocks](#finding-races-leaks-and-deadlocks)
13. [Anti-patterns](#anti-patterns)

## Rules that prevent most bugs

1. **Every goroutine has an owner and an exit path.** The owner is the code that
   started it, and it can stop it and wait for it. If nobody can answer "what makes
   this goroutine return?", it leaks.
2. **Every blocking send or receive also selects on `ctx.Done()`.** A worker that
   does `out <- v` with no `select` hangs forever once the reader is gone.
3. **Only the sender closes a channel, and only once.** A receiver that closes it
   panics the sender ("send on closed channel").
4. **Bound concurrency.** One goroutine per request item, with no limit, turns a
   traffic spike into an out-of-memory crash or a flood on the downstream service.
5. **Prefer `errgroup` to a hand-rolled `WaitGroup` plus error channel.** You get
   error propagation, cancellation and limits in about ten lines.
6. **Don't use `time.Sleep` for synchronization.** Use channels, `WaitGroup`, or
   `synctest.Wait` in tests.

## Choosing a primitive

| Need | Use | Notes |
|---|---|---|
| Run N tasks, stop on first error | `errgroup.WithContext` + `SetLimit` | Default choice for fan-out |
| Long-lived consumers of a queue | Worker pool (below) | Fixed goroutine count, ctx-aware sends |
| Protect a small piece of shared state | `sync.Mutex` | Simplest and fastest under low contention |
| Read-mostly config/cache, rarely written | `sync.RWMutex` or `atomic.Pointer[T]` | `atomic.Pointer` swaps an immutable snapshot |
| Counter, flag | `atomic.Int64`, `atomic.Bool` | Typed atomics avoid misaligned 64-bit access |
| Wait for goroutines, no errors | `sync.WaitGroup` with `wg.Go` | 1.25+ |
| One-time init | `sync.OnceValue(s)` | Returns the value and makes init race-free |
| Cap in-flight cost | `semaphore.Weighted` | Cost can vary per job |
| Requests per second | `rate.Limiter` | Token bucket |
| Many callers want the same key | `singleflight.Group` | Stops a thundering herd on cache misses |
| Hand off ownership of data | Channel | "Share memory by communicating" |

## Bounded fan-out with errgroup

```go
// Package fanout calls a dependency for many keys with bounded concurrency.
package fanout

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"
)

// FetchAll runs fetch for every id, at most limit at a time, and returns the
// results in input order. The first error cancels ctx for the others.
func FetchAll[T any](ctx context.Context, ids []string, limit int, fetch func(context.Context, string) (T, error)) ([]T, error) {
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(max(limit, 1)) // g.Go blocks while limit goroutines run; 0 would block forever

	out := make([]T, len(ids))
	for i, id := range ids {
		if ctx.Err() != nil {
			break // a task already failed; don't start the rest
		}
		g.Go(func() error {
			v, err := fetch(ctx, id)
			if err != nil {
				return fmt.Errorf("fetch %s: %w", id, err)
			}
			out[i] = v // each goroutine writes its own index: no mutex needed
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}
```

## Worker pool

Use a pool when jobs arrive over time, for example from a queue or a stream RPC,
and you want a fixed number of consumers. It has two exit paths: the input closes,
or ctx is cancelled. Both are checked at every blocking point.

```go
// Package pool runs a fixed number of workers over a stream of jobs.
package pool

import (
	"context"
	"sync"
)

// Result pairs an output with its error so one bad job doesn't stop the pool.
type Result[In, Out any] struct {
	Job In
	Val Out
	Err error
}

// Run starts workers that consume jobs until jobs is closed or ctx is done.
// The returned channel closes after every worker exits, so ranging over it is
// the caller's wait. The caller must drain it or cancel ctx.
func Run[In, Out any](ctx context.Context, workers int, jobs <-chan In, fn func(context.Context, In) (Out, error)) <-chan Result[In, Out] {
	out := make(chan Result[In, Out])
	var wg sync.WaitGroup
	for range max(workers, 1) { // zero workers would never consume jobs
		wg.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					v, err := fn(ctx, job)
					select {
					case out <- Result[In, Out]{Job: job, Val: v, Err: err}:
					case <-ctx.Done(): // reader is gone: don't block forever
						return
					}
				}
			}
		})
	}
	go func() {
		wg.Wait()
		close(out) // the only sender-side close, after all senders finished
	}()
	return out
}
```

## Pipelines and fan-in

Each stage owns the channel it returns and closes it when done. To fan out, start
the same stage several times on one input channel. To fan in, merge their outputs.

```go
// Package pipeline provides cancellable, generic pipeline stages.
package pipeline

import (
	"context"
	"sync"
)

// From emits items then closes.
func From[T any](ctx context.Context, items ...T) <-chan T {
	out := make(chan T)
	go func() {
		defer close(out)
		for _, it := range items {
			select {
			case out <- it:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// Map applies fn to every value. Start it N times on one input to fan out.
func Map[In, Out any](ctx context.Context, in <-chan In, fn func(In) Out) <-chan Out {
	out := make(chan Out)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case v, ok := <-in:
				if !ok {
					return
				}
				select {
				case out <- fn(v):
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

// Merge fans in: it forwards every input until all are closed.
func Merge[T any](ctx context.Context, ins ...<-chan T) <-chan T {
	out := make(chan T)
	var wg sync.WaitGroup
	for _, in := range ins {
		wg.Go(func() {
			for {
				select { // receive and send both honour ctx
				case <-ctx.Done():
					return
				case v, ok := <-in:
					if !ok {
						return
					}
					select {
					case out <- v:
					case <-ctx.Done():
						return
					}
				}
			}
		})
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}
```

Unbuffered channels give natural backpressure, because a slow stage slows the
ones before it. Add a buffer only when you've measured that a fast and a slow
stage are bursty. A buffer smooths bursts; it doesn't add throughput.

## Admission control: semaphores and rate limits

```go
// Package admission bounds in-flight cost and request rate for a dependency.
package admission

import (
	"context"
	"fmt"

	"golang.org/x/sync/semaphore"
	"golang.org/x/time/rate"
)

// Gate admits work when both budgets allow it, or fails when ctx ends.
type Gate struct {
	maxCost int64
	sem     *semaphore.Weighted
	lim     *rate.Limiter
}

// NewGate caps total in-flight cost (e.g. MB of memory) and starts per second.
func NewGate(maxCost int64, perSecond float64, burst int) *Gate {
	return &Gate{
		maxCost: maxCost,
		sem:     semaphore.NewWeighted(maxCost),
		lim:     rate.NewLimiter(rate.Limit(perSecond), burst),
	}
}

// Do runs fn once admitted. A cost above maxCost could never be admitted and
// would block until ctx ends, so it is rejected up front.
func (g *Gate) Do(ctx context.Context, cost int64, fn func(context.Context) error) error {
	if cost > g.maxCost {
		return fmt.Errorf("cost %d exceeds gate capacity %d", cost, g.maxCost)
	}
	if err := g.lim.Wait(ctx); err != nil {
		return err
	}
	if err := g.sem.Acquire(ctx, cost); err != nil {
		return err
	}
	defer g.sem.Release(cost)
	return fn(ctx)
}
```

When every job costs the same, `sem := make(chan struct{}, n)` with
`sem <- struct{}{}` / `<-sem` works too. Put the send inside a `select` with
`ctx.Done()` so waiting stays cancellable.

## Coalescing duplicate work with singleflight

```go
// Package cache loads values once per key even under concurrent misses.
package cache

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Loader coalesces concurrent loads of one key into a single backend call.
// Add TTL/eviction for production use; this keeps entries forever.
type Loader[V any] struct {
	group singleflight.Group
	mu    sync.RWMutex
	data  map[string]V
	load  func(context.Context, string) (V, error)
}

// NewLoader wraps load with coalescing and caching.
func NewLoader[V any](load func(context.Context, string) (V, error)) *Loader[V] {
	return &Loader[V]{data: make(map[string]V), load: load}
}

// Get returns the cached value or loads it. Each caller still honours its own
// ctx: DoChan lets a cancelled caller leave without cancelling the shared load.
// DoChan runs the load on its own goroutine, where a panic would crash the
// process past any gRPC recovery interceptor, so the load recovers itself.
func (l *Loader[V]) Get(ctx context.Context, key string) (V, error) {
	l.mu.RLock()
	v, ok := l.data[key]
	l.mu.RUnlock()
	if ok {
		return v, nil
	}

	ch := l.group.DoChan(key, func() (val any, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("load %q panicked: %v", key, r)
			}
		}()
		// Shared work must not die with the first caller's cancellation.
		loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		v, err := l.load(loadCtx, key)
		if err != nil {
			return nil, err
		}
		l.mu.Lock()
		l.data[key] = v
		l.mu.Unlock()
		return v, nil
	})

	var zero V
	select {
	case r := <-ch:
		if r.Err != nil {
			return zero, r.Err
		}
		v, _ := r.Val.(V) // comma-ok: a nil interface V would fail a plain assertion
		return v, nil
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}
```

## Shared maps

Start with a plain `map` behind a `sync.Mutex`. Switch only after a profile
shows lock contention:
- **`sync.Map`** suits keys that are written once and read many times, or
  goroutines that work on disjoint key sets. It was rewritten in 1.24 and
  scales much better.
- **A sharded map** suits write-heavy workloads.

The usual hand-written shard hash, `h = 31*h + c` followed by `h % n` on an
`int`, can overflow to a negative number and panic with an index out of range.
Hash to `uint64` with `maphash` instead.

```go
// Package shardmap is a generic map split into independently locked shards.
package shardmap

import (
	"hash/maphash"
	"sync"
)

// Map reduces lock contention for write-heavy workloads.
type Map[K comparable, V any] struct {
	seed   maphash.Seed
	shards []shard[K, V]
}

type shard[K comparable, V any] struct {
	mu sync.RWMutex
	m  map[K]V
}

// New creates a map with n shards (a small power of two such as 32 is typical).
func New[K comparable, V any](n int) *Map[K, V] {
	n = max(n, 1) // zero shards would divide by zero in shardFor
	m := &Map[K, V]{seed: maphash.MakeSeed(), shards: make([]shard[K, V], n)}
	for i := range m.shards {
		m.shards[i].m = make(map[K]V)
	}
	return m
}

func (m *Map[K, V]) shardFor(k K) *shard[K, V] {
	// uint64 modulo is never negative. maphash.Comparable needs Go 1.24+.
	return &m.shards[maphash.Comparable(m.seed, k)%uint64(len(m.shards))]
}

// Get returns the value for k.
func (m *Map[K, V]) Get(k K) (V, bool) {
	s := m.shardFor(k)
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.m[k]
	return v, ok
}

// Set stores v under k.
func (m *Map[K, V]) Set(k K, v V) {
	s := m.shardFor(k)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = v
}
```

## Timed loops and deterministic tests with synctest

Inside a `select` loop, create one `Ticker` or `Timer` and reuse it. Calling
`time.After` on every iteration allocates a new timer each time.

`testing/synctest` (1.25) runs a test in a "bubble" with a fake clock. Time
advances only when every goroutine in the bubble is blocked. Tests of timeouts,
retries and tickers then finish instantly and produce the same result every run.

```go
package poller

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

// Poll calls check every interval until it reports done, fails, or ctx ends.
func Poll(ctx context.Context, interval time.Duration, check func(context.Context) (bool, error)) error {
	t := time.NewTicker(interval) // one ticker for the whole loop
	defer t.Stop()
	for {
		done, err := check(ctx)
		if err != nil || done {
			return err
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-t.C:
		}
	}
}

func TestPollStopsWhenDone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start, calls := time.Now(), 0
		err := Poll(t.Context(), time.Minute, func(context.Context) (bool, error) {
			calls++
			return calls == 3, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := time.Since(start); got != 2*time.Minute { // exact: the clock is fake
			t.Errorf("elapsed %v, want 2m", got)
		}
	})
}

func TestPollHonoursDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()
		err := Poll(ctx, time.Minute, func(context.Context) (bool, error) { return false, nil })
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want DeadlineExceeded", err)
		}
	})
}
```

## Owned background workers

Some loops run for the whole process lifetime: outbox relays, cache refreshers,
queue consumers. Give each a type that starts them and can stop them. Then
`main` stops them in a known order during shutdown.

```go
// Package relay shows a background worker with an explicit owner.
package relay

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Relay periodically flushes pending work until stopped.
type Relay struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Start launches the loop; every must be > 0 (time.NewTicker panics otherwise).
// It derives its own ctx so Stop works even when the parent lives on.
func Start(parent context.Context, every time.Duration, flush func(context.Context) error, logger *slog.Logger) *Relay {
	ctx, cancel := context.WithCancel(parent)
	r := &Relay{cancel: cancel}
	r.wg.Go(func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := flush(ctx); err != nil {
					logger.ErrorContext(ctx, "relay flush failed", "err", err)
				}
			}
		}
	})
	return r
}

// Stop cancels the loop and waits for it, up to ctx's deadline. Cancelling
// also aborts an in-flight flush and no final flush runs; for an outbox, call
// flush once more after Stop if pending work must not wait for the next start.
func (r *Relay) Stop(ctx context.Context) error {
	r.cancel()
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("relay stop: %w", ctx.Err())
	}
}
```

## Work that outlives the request

The RPC context is cancelled the moment the handler returns. Work started with
`go f(ctx)` that keeps using that ctx fails at random with `context canceled`.
For fire-and-forget work, such as audit records, cache warming or notifications:
- detach with `context.WithoutCancel`, which keeps the trace context and other
  values;
- give the work its own timeout;
- cap how many of these can be in flight;
- make the server wait for them during shutdown;
- start a **new root span linked** to the request, so the request's trace
  duration stays honest.

```go
// Package audit records events after the RPC has already replied.
package audit

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Recorder runs bounded, detached writes.
type Recorder struct {
	wg     sync.WaitGroup
	slots  chan struct{}
	write  func(context.Context, string) error
	tracer trace.Tracer
	logger *slog.Logger
}

// NewRecorder allows at most maxInFlight (> 0) detached writes; beyond that,
// events are dropped rather than slowing down RPCs.
func NewRecorder(maxInFlight int, write func(context.Context, string) error, tp trace.TracerProvider, logger *slog.Logger) *Recorder {
	return &Recorder{
		slots:  make(chan struct{}, maxInFlight),
		write:  write,
		tracer: tp.Tracer("example.com/audit"),
		logger: logger,
	}
}

// RecordAsync never blocks the RPC: when the backlog is full it sheds load.
func (r *Recorder) RecordAsync(ctx context.Context, event string) {
	select {
	case r.slots <- struct{}{}:
	default:
		r.logger.WarnContext(ctx, "audit backlog full, dropping event", "event", event)
		return
	}
	link := trace.LinkFromContext(ctx)
	detached := context.WithoutCancel(ctx)
	r.wg.Go(func() {
		defer func() { <-r.slots }()
		ctx, cancel := context.WithTimeout(detached, 5*time.Second)
		defer cancel()
		ctx, span := r.tracer.Start(ctx, "audit.write", trace.WithNewRoot(), trace.WithLinks(link))
		defer span.End()
		if err := r.write(ctx, event); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "audit write failed")
			r.logger.ErrorContext(ctx, "audit write failed", "err", err)
		}
	})
}

// Wait blocks until in-flight writes finish. Call it after the gRPC server
// has stopped, so no new RecordAsync calls race with it.
func (r *Recorder) Wait() { r.wg.Wait() }
```

## Finding races, leaks and deadlocks

Production leak detection and test-based proofs (goleak, leak tests, heap
diffs) are in `references/performance-and-memory.md`. This section covers
the concurrency-specific tools.

```sh
go test -race -count=1 ./...                       # in CI, always; races only show when exercised
curl -s 'localhost:6060/debug/pprof/goroutine?debug=2' > goroutines.txt   # who is blocked where
GOEXPERIMENT=goroutineleakprofile go build ./cmd/server                 # Go 1.26 experiment
curl -s localhost:6060/debug/pprof/goroutineleak                        # Go 1.27+: GA, no experiment flag
```

- **Leak symptoms:** the goroutine count grows with traffic and never comes
  back down. You can read it from `runtime.NumGoroutine()`, or from the
  `go.goroutine.count` metric that the OTel runtime instrumentation exports. In a goroutine dump, group stacks by their top frame. Hundreds parked at
  the same `chan send` line is the leak.
- **Partial deadlocks:** the runtime prints "all goroutines are asleep" only
  when *every* goroutine is blocked. A service whose request goroutines are
  stuck on a lock or channel keeps running and looks healthy. Find these in
  goroutine dumps and the mutex profile
  (`runtime.SetMutexProfileFraction(5)`).
- **In tests:**
  - `synctest.Test` waits for every goroutine started in the bubble. If any of
    them stays blocked, the test fails as deadlocked, so you get leak detection
    for free.
  - For code outside a bubble, `go.uber.org/goleak` with
    `goleak.VerifyTestMain(m)` checks for leftover goroutines.

## Anti-patterns

| Anti-pattern | What breaks | Fix |
|---|---|---|
| `results <- r` with no `select` on ctx | Workers block forever when the reader returns early, which leaks the goroutine and the memory it holds | `select { case out <- r: case <-ctx.Done(): return }` |
| `make(chan T, len(jobs))` where `jobs` is a channel | `len` is how many items are buffered right now, often 0. The capacity is meaningless | Size from known counts or leave unbuffered |
| Closing a channel from the receiver | The sender panics | Only the sender closes; signal the sender with ctx |
| `go handle(ctx, req)` in an RPC handler | ctx is cancelled when the handler returns | `context.WithoutCancel` + timeout + owner WaitGroup |
| Unbounded `go` per item | A traffic spike causes OOM or overloads the downstream | `errgroup.SetLimit`, worker pool, semaphore |
| Mutex around writes to distinct slice indices | Pointless contention | Each goroutine writes `out[i]` without a lock |
| `wg.Add(1)` inside the goroutine | `Wait` can return before `Add` runs | `wg.Go(f)` (1.25), or `Add` before `go` |
| Copying a struct that contains a `sync.Mutex` | Two independent locks; vet reports `copylocks` | Pass pointers, keep the mutex next to its data |
| `time.Sleep` to "wait for" a goroutine | Flaky tests, wasted time | Channels, `WaitGroup`, `synctest.Wait` |
| `sync.Map` as the default concurrent map | Slower and less type-safe for general workloads | Mutex + map until a profile says otherwise |
