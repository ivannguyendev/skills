# Performance, memory and leaks

How to make a Go server fast, keep its memory flat under load, and prove it
does not leak. The pattern throughout: measure, fix the biggest item, then lock
the gain in with a test.

## Contents

1. [Measure first](#measure-first)
2. [Profiling recipes](#profiling-recipes)
3. [Allocation rules for hot paths](#allocation-rules-for-hot-paths)
4. [sync.Pool rules](#syncpool-rules)
5. [Memory shape: what the GC has to scan](#memory-shape-what-the-gc-has-to-scan)
6. [GC and runtime tuning](#gc-and-runtime-tuning)
7. [Profile-guided optimization](#profile-guided-optimization)
8. [Leak classes](#leak-classes)
9. [Finding leaks in production](#finding-leaks-in-production)
10. [Proving it in tests](#proving-it-in-tests)

## Measure first

1. **State the target.** For example: p99 < 50 ms at 2,000 RPS per instance,
   memory under 70% of the limit. Without a number, "optimize" never ends.
2. **Reproduce the load.** Use a load generator (k6, vegeta, ghz for gRPC)
   against a production-like build, with production config and flags.
3. **Profile under that load.** Take CPU, allocs, mutex, block and an execution
   trace from the admin port.
4. **Fix the top item only, then measure again.** Most wins come from removing
   work: round trips, allocations in loops, logging, lock contention.
5. **Lock it in.** Add a benchmark compared with `benchstat`, plus an allocation
   budget test, so the next change can't silently undo the gain.

Optimizing without a profile usually makes code harder to read and changes
nothing that users notice.

## Profiling recipes

The skeleton serves pprof on the private admin port (`internal/admin`), never
on the public listener.

```sh
# CPU for 30s under load, opened in the browser UI
go tool pprof -http=:0 'http://localhost:9090/debug/pprof/profile?seconds=30'

# Allocation hot spots (bytes allocated since start), then live heap
go tool pprof -sample_index=alloc_space http://localhost:9090/debug/pprof/allocs
go tool pprof -sample_index=inuse_space http://localhost:9090/debug/pprof/heap

# What grew between two moments: diff heap profiles
curl -so before.pb.gz http://localhost:9090/debug/pprof/heap
curl -so after.pb.gz  http://localhost:9090/debug/pprof/heap      # minutes later, under load
go tool pprof -base before.pb.gz after.pb.gz

# Goroutines grouped by stack: the fastest way to see a leak or a pile-up
curl -s 'http://localhost:9090/debug/pprof/goroutine?debug=1' | head -50

# Scheduler, GC and blocking over 5s
curl -so trace.out 'http://localhost:9090/debug/pprof/trace?seconds=5' && go tool trace trace.out
```

- **Mutex and block profiles are empty by default.** Enable them with
  `runtime.SetMutexProfileFraction(100)` and `runtime.SetBlockProfileRate(10000)`
  at startup, at a sampling rate cheap enough for production.
- **Flight recorder (1.25).** Continuous tracing is too expensive, and by the
  time you start a trace the slow request is gone. A flight recorder keeps the
  last few seconds of execution trace in memory, and you snapshot it when
  something slow happens.

```go
// Package flight snapshots the runtime flight recorder when a request is slow.
package flight

import (
	"bytes"
	"fmt"
	"log/slog"
	"runtime/trace"
	"sync"
	"time"
)

// Recorder keeps the last few seconds of execution trace and saves at most
// one snapshot per interval, so a slow period cannot flood the disk.
type Recorder struct {
	fr       *trace.FlightRecorder
	mu       sync.Mutex
	last     time.Time
	interval time.Duration
	save     func([]byte) error // e.g. write to a file or upload to blob storage
}

// Start begins recording; MaxBytes bounds the memory the recorder uses.
func Start(save func([]byte) error) (*Recorder, error) {
	fr := trace.NewFlightRecorder(trace.FlightRecorderConfig{MinAge: 5 * time.Second, MaxBytes: 16 << 20})
	if err := fr.Start(); err != nil {
		return nil, fmt.Errorf("start flight recorder: %w", err)
	}
	return &Recorder{fr: fr, interval: time.Minute, save: save}, nil
}

// Slow is called by middleware when a request exceeds its latency budget.
// Only the caller that claims the interval pays for the snapshot; the lock is
// released first, so concurrent slow requests return immediately instead of
// queueing behind a 16 MiB write and an upload.
func (r *Recorder) Slow(elapsed time.Duration) {
	r.mu.Lock()
	if time.Since(r.last) < r.interval {
		r.mu.Unlock()
		return
	}
	r.last = time.Now()
	r.mu.Unlock()

	var buf bytes.Buffer
	if _, err := r.fr.WriteTo(&buf); err != nil {
		slog.Warn("flight recorder snapshot failed", "err", err)
		return
	}
	if err := r.save(buf.Bytes()); err != nil {
		slog.Warn("flight recorder save failed", "err", err, "elapsed", elapsed)
	}
}

// Stop ends recording.
func (r *Recorder) Stop() { r.fr.Stop() }
```

## Allocation rules for hot paths

Every allocation is GC work later. At high RPS, allocation rate, not CPU, is
often what drives tail latency.

- **Preallocate** when the size is known or bounded: `make([]T, 0, n)`,
  `slices.Grow`, `make(map[K]V, n)`, `strings.Builder.Grow`.
- **No `fmt.Sprintf` for keys, headers or metrics.** Use `strconv.Append*`
  into a reused or stack buffer, or concatenation. The `perfsprint` linter
  flags the easy cases.
- **Avoid boxing into `any`.** `slog.LogAttrs` with typed attributes,
  generic helpers instead of `interface{}` parameters, and no
  `map[string]any` payloads.
- **No reflection per request.** Reflection-based validation, mapping and
  binding libraries allocate heavily. Use hand-written validation in the domain
  or schema validation at the edge (protovalidate for gRPC).
- **Check escape analysis:** `go build -gcflags=-m=2 ./internal/... 2>&1 | grep escapes`
  shows what moves to the heap and why.
- **Pick the serialization by profile.** `encoding/json` with a pooled buffer
  is the default. `encoding/json/v2` needs a 1.27 go line. Code-generated or
  SIMD JSON libraries only when a profile shows JSON dominating CPU. Protobuf
  for internal traffic.

```go
// Package keys builds cache keys without allocating intermediate strings.
package keys

import "strconv"

// Order returns "order:<tenant>:<id>" with a single allocation (the result).
// fmt.Sprintf would allocate the result plus every boxed argument.
func Order(tenant string, id int64) string {
	var buf [64]byte // stays on the stack
	b := append(buf[:0], "order:"...)
	b = append(b, tenant...)
	b = append(b, ':')
	b = strconv.AppendInt(b, id, 10)
	return string(b)
}
```

Pin it the way the skeleton pins `WriteJSON`: a `keys_alloc_test.go` with
`//go:build !race` asserting `testing.AllocsPerRun(...) <= 1`, plus a
`BenchmarkOrder` using `b.Loop()` and `b.ReportAllocs()`. See
`internal/httpserver/respond_alloc_test.go`.

## sync.Pool rules

`internal/httpserver/respond.go` shows a correct pool.

- **Pool pointers** (`*bytes.Buffer`, `*jsonBuf`), never values. Putting a
  value into an `any` allocates, which defeats the pool.
- **Reset on Get, and keep no reference after Put.** A buffer still referenced
  after `Put` is used by two requests at once, a data race the race detector
  only sometimes catches.
- **Drop oversized objects instead of returning them.** One 10 MiB response
  would otherwise keep 10 MiB alive in the pool indefinitely. The skeleton caps
  pooled buffers at 64 KiB.
- **Pool only short-lived, same-shaped, frequently allocated objects** that a
  profile shows are hot. A pool is not a cache: the GC empties it at any time.

## Memory shape: what the GC has to scan

- **Pointers cost GC time.** A 10-million-entry `map[string]*Item` has 10
  million pointers to trace on every cycle. For large in-memory datasets,
  prefer values (`map[int64]Item`), indices into slices, or `[]byte` blobs.
- **Sub-slices keep the whole array alive.** `small := big[:16]` retains all
  of `big`. This is common with Kafka/NATS message buffers and with parsed
  bodies. Copy what you keep with `bytes.Clone`, `strings.Clone` or
  `slices.Clone`.
- **Maps never shrink.** Deleting keys does not return bucket memory. A map
  that once held a million entries stays that big; rebuild it if it routinely
  spikes.
- **`q = q[1:]` as a queue** keeps the head of the backing array reachable
  until reallocation. Use a ring buffer or a bounded channel.
- **Bound every cache by entry count or bytes, with a TTL.** An unbounded
  `map` used as a cache is the most common slow leak in Go services.

## GC and runtime tuning

| Knob | Recommendation | Why |
|---|---|---|
| `GOMEMLIMIT` | About 90% of the container memory limit, e.g. `GOMEMLIMIT=900MiB` for 1 GiB | Without it, the GC sizes the heap only by GOGC and can let it grow past the container limit, so the OOM killer acts before the GC does. It is a *soft* limit: Go caps GC CPU at about 50% rather than thrash, so keep 5–10% headroom |
| `GOGC` | Leave it at 100. Raise it (200–400) only when GC CPU is high and the heap is far below the limit | Trades memory for less GC work. Never use `GOGC=off` on servers without a memory limit |
| `GOMAXPROCS` | Leave it alone on 1.25+ | It follows the container CPU limit automatically; remove `automaxprocs` |
| CPU limits | Always set CPU *requests*. For latency-sensitive services, leave out the CPU limit or set it generously | A limit means CFS throttling: once the quota is used, the whole process pauses for the rest of the 100 ms period, which shows up as p99 spikes. On 1.25+ GOMAXPROCS follows the limit, which reduces but does not remove throttling |
| Green Tea GC | Default on 1.26+ | Typically 10–40% less GC CPU; nothing to configure |

Heap ballast (a large unused allocation to delay GC) is obsolete since
`GOMEMLIMIT`. Remove it.

## Profile-guided optimization

PGO lets the compiler inline and lay out code based on a real CPU profile.
Typical gain is 2–14% CPU, with no code changes.

1. Under representative load, collect `curl -so cmd/server/default.pgo
   'http://localhost:9090/debug/pprof/profile?seconds=30'`.
   Merge several with `go tool pprof -proto a b > default.pgo`.
2. Commit `default.pgo` next to `main.go`. `go build` uses it automatically
   (`-pgo=auto`), including in the skeleton's Dockerfile.
3. Refresh it every few releases. A stale profile still helps but drifts.

## Leak classes

A Go "memory leak" is almost always memory or goroutines that are still
reachable. Each class below has a structural fix, and the skeleton applies the
fix where it has the code.

| Class | Typical cause | Structural fix |
|---|---|---|
| Goroutines without an owner | `go f()` in a handler or constructor; a send that nobody receives | Owner type with `Run(ctx)`/`Close`. Every channel operation selects on `ctx.Done()`. See `references/concurrency-patterns.md` |
| Missing timeouts | `http.Server{}`, `http.DefaultClient`, calls without deadlines | `internal/httpserver` defaults, a tuned outbound client, `Deadline` middleware, gRPC per-call deadlines |
| Unclosed resources | Response bodies, `sql.Rows`, gRPC client streams, tickers | `defer Close` right after the error check; drain HTTP bodies; the `bodyclose` and `sqlclosecheck` linters |
| Lost `cancel` | `ctx, _ := context.WithTimeout(...)` | `defer cancel()` always (`go vet lostcancel`) |
| Unbounded growth | Maps keyed by request data (per-IP limiters, dedupe sets), unbounded queues and caches | Size limit + TTL on every keyed structure; dedupe in a database instead of memory |
| Retention | Sub-slices of large buffers, `sync.Pool` holding huge objects, maps that never shrink | Clone what you keep; cap pooled sizes; rebuild maps |
| Overload queueing | Waiters pile up when capacity is exceeded | `resilience.Limiter` with a bounded queue wait (`internal/resilience`) |
| Telemetry cardinality | IDs in span names, metric labels or log keys | Route patterns (`RouteTag`), bounded label sets |
| Client buffers | Producers buffering without limit while a broker is down | Explicit max buffered records/bytes on every client |
| cgo memory | C allocations invisible to the GC and to `GOMEMLIMIT` | `CGO_ENABLED=0` unless a dependency truly needs it |

## Finding leaks in production

- **Graph three things per instance:**
  - goroutine count (`/sched/goroutines:goroutines`, exported by the OTel
    runtime instrumentation as `go.goroutine.count`);
  - live heap (`/memory/classes/heap/objects:bytes`);
  - RSS.

  A leak looks like a line that rises with traffic and never comes back down
  between peaks.
- **Goroutine dump, grouped:** `/debug/pprof/goroutine?debug=1` groups
  identical stacks with a count. Hundreds of goroutines parked on the same
  `chan send` or `select` line is the leak.
- **`goroutineleak` profile** (an experiment in 1.26 with
  `GOEXPERIMENT=goroutineleakprofile`; GA in 1.27): reports goroutines blocked
  forever on primitives that nothing else can reach.
- **Heap diff:** `go tool pprof -base` between two snapshots taken minutes
  apart under steady load. What remains is what accumulates.
- **RSS high but heap flat:** check for cgo, `mmap`ed files, or memory the
  runtime returns to the OS only lazily. `/memory/classes/total:bytes` breaks
  it down.

## Proving it in tests

The skeleton runs four kinds of check.

1. **`goleak.VerifyTestMain`** in every package that starts goroutines. After
   all tests finish, any goroutine still running fails the package. Each test
   closes what it opened and calls `client.CloseIdleConnections()`.
2. **A leak test under adversarial load**
   (`internal/httpserver/leak_test.go`). It hammers the full chain with every
   outcome: success, 4xx, 413, panic, shed 503, client cancellation, deadline.
   It then requires the goroutine count to return to its baseline and the
   limiter to hold no slots. It waits for a stable count (`settle`) instead of
   sleeping a fixed time, so it doesn't flake, and it runs under `-race`.
3. **Allocation budgets** (`*_alloc_test.go`, `//go:build !race` because race
   instrumentation changes counts):
   - the limiter takes 0 allocations;
   - `WriteJSON` takes 3.

   A budget is pinned at the measured value, so any regression shows up in
   review.
4. **Benchmarks** with `b.Loop()` and `b.ReportAllocs()`. Compare runs with
   `go test -bench . -count=10 > new.txt` and `benchstat old.txt new.txt`.
   Assert allocations, never ns/op, because timing varies across machines.

For a longer soak, run the service under a load generator for 30–60 minutes
at production rate. The heap and goroutine graphs from the admin port should
reach a plateau, not keep rising.
