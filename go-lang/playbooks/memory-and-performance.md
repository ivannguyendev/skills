# Memory and performance

## Runtime

| Knob | Setting | Why |
|---|---|---|
| `GOMEMLIMIT` | ≈90% of the limit (`1Gi` → `900MiB`) | Without it the heap outgrows the limit and the OOM killer wins. It's a soft limit, so leave headroom for non-heap memory |
| `GOGC` | 100. Use 200–400 only if GC CPU is high *and* the heap is far below the limit | Never `GOGC=off` without a memory limit |
| CPU limit | Set requests. Latency-sensitive services: no limit, or a generous one | CFS throttling pauses the process until the next 100 ms period, which shows as p99 spikes |

Set `GOMEMLIMIT` in the manifest; `resourceFieldRef` gives 100%, not 90%. As a fallback, call this at the start of `main`:

```go
func setMemLimit() { // 90% of the cgroup v2 limit when GOMEMLIMIT is unset
	if os.Getenv("GOMEMLIMIT") != "" {
		return
	}
	b, err := os.ReadFile("/sys/fs/cgroup/memory.max")
	if err != nil {
		return
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return // "max": no limit
	}
	debug.SetMemoryLimit(n / 10 * 9)
}
```

## Measure first

Set a target, run an open-model load on a production build, profile from the admin port, fix only the top item, re-measure, then lock it in with `benchstat -count=10` and an allocation budget.

```sh
go tool pprof -http=:0 'http://admin:9090/debug/pprof/profile?seconds=30'   # CPU
go tool pprof -sample_index=alloc_space http://admin:9090/debug/pprof/allocs # churn
go tool pprof -sample_index=inuse_space http://admin:9090/debug/pprof/heap   # live
go tool pprof -base a.pb.gz b.pb.gz          # heap diff, snapshots minutes apart
curl -s 'http://admin:9090/debug/pprof/goroutine?debug=1' | head -60   # grouped stacks
curl -s http://admin:9090/debug/pprof/goroutineleak                    # Go 1.27+
go build -gcflags=-m=2 ./... 2>&1 | grep escapes
```

PGO: commit `cmd/server/default.pgo` (2–14% CPU) and refresh it every few releases.

## Hot paths

At high RPS, the allocation rate drives tail latency.
- Preallocate (`make(…, 0, n)`, `slices.Grow`).
- No `fmt.Sprintf` for keys or labels: use `strconv.Append*` into a `[64]byte` buffer.
- No boxing: `slog.LogAttrs`, no `map[string]any`, no per-request reflection (binding, validation, mappers).
- By default, log only errors and slow requests.

## sync.Pool

```go
var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

const maxPooled = 64 << 10

func encode(v any) ([]byte, error) {
	b := bufPool.Get().(*bytes.Buffer) // pointer: Put of a value allocates
	b.Reset()
	defer func() {
		if b.Cap() <= maxPooled { // don't pin huge buffers
			bufPool.Put(b)
		}
	}()
	if err := json.NewEncoder(b).Encode(v); err != nil {
		return nil, err
	}
	return bytes.Clone(b.Bytes()), nil // never hand out pooled memory
}
```

Pool only hot, short-lived, same-shaped objects; the GC empties pools at any time.

## Leak classes

| Symptom | Cause | Fix |
|---|---|---|
| Goroutines rise with traffic | Channel op without ctx; `go` in a handler or constructor | Owner + `Run(ctx)`, `select` on ctx |
| Goroutines and FDs pile up | No timeouts or deadlines | Explicit timeouts, deadline middleware |
| Connections exhausted | Unclosed body, rows, stream or ticker | `defer Close`; `bodyclose`/`sqlclosecheck` linters |
| Heap rises forever | Request-keyed map, cache or per-IP limiter | Cap + TTL |
| Heap high, never released | Sub-slice pins its array; oversized pooled buffer; map never shrinks; `q = q[1:]` | `bytes.Clone`, pool cap, rebuild the map, ring buffer |
| Memory and latency blow up on spikes | Unbounded queueing | Limiter → 503 |
| SDK/collector memory grows | IDs in span names or labels | Route patterns |
| RSS ≫ heap | cgo, mmap | `CGO_ENABLED=0`; check `/memory/classes/total:bytes` |

## Leak hunt

1. Graph goroutines, live heap and RSS per instance. A leak rises with traffic and never falls back between peaks.
2. In `goroutine?debug=1`, hundreds of goroutines parked on one `chan send`/`select` line are the leak.
3. Diff the heap with `-base` under steady load.
4. Reproduce the leak in a test, then fix the structure, not the limit.

## Proving it in tests

- `goleak.VerifyTestMain(m)`.
- Leak test under mixed outcomes (2xx/4xx/413/panic/503/cancel/deadline): the goroutine count returns to baseline (poll, don't sleep) and the limiter holds 0 slots.
- `*_alloc_test.go` with `//go:build !race`: `testing.AllocsPerRun(100, f) <= budget`.
- Benchmarks use `for b.Loop()` + `b.ReportAllocs()`. Assert allocations, never ns/op.
