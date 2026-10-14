# Concurrency safety

## Choosing a primitive

| Need | Use |
|---|---|
| N tasks, stop on first error | `errgroup.WithContext` + `SetLimit` |
| Long-lived consumers | Worker pool (below) |
| Small shared state | `sync.Mutex` next to the data (`sync.Map` is not the default) |
| Read-mostly snapshot | `atomic.Pointer[T]` holding an immutable value |
| Lazy init | `sync.OnceValue(s)` |
| Variable cost per job | `semaphore.Weighted` |
| Many callers, one key | `singleflight.DoChan` |

## Bounded fan-out

```go
g, ctx := errgroup.WithContext(ctx)
g.SetLimit(max(limit, 1)) // 0 blocks forever
out := make([]T, len(ids))
for i, id := range ids {
	if ctx.Err() != nil {
		break // a task failed; don't start the rest
	}
	g.Go(func() error {
		v, err := fetch(ctx, id)
		if err != nil {
			return fmt.Errorf("fetch %s: %w", id, err)
		}
		out[i] = v // own index: no lock
		return nil
	})
}
if err := g.Wait(); err != nil {
	return nil, err
}
```

## Worker pool

```go
func Run[In, Out any](ctx context.Context, workers int, jobs <-chan In,
	fn func(context.Context, In) (Out, error)) <-chan Result[Out] {
	out := make(chan Result[Out])
	var wg sync.WaitGroup
	for range max(workers, 1) {
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
					case out <- Result[Out]{Val: v, Err: err}:
					case <-ctx.Done(): // reader gone
						return
					}
				}
			}
		})
	}
	go func() { wg.Wait(); close(out) }() // sender closes, once
	return out
}
```

## Work after the response

For audit, notification or cache-warm work, the goroutine is:
- detached with `context.WithoutCancel`;
- given its own timeout;
- capped by a slot channel (drop and count when full);
- tracked by a `WaitGroup` that shutdown waits on;
- traced as a new root span linked to the request (`trace.WithNewRoot()`, `trace.WithLinks(trace.LinkFromContext(ctx))`).

```go
func (r *Recorder) RecordAsync(ctx context.Context, ev Event) {
	select {
	case r.slots <- struct{}{}:
	default:
		r.dropped.Add(1) // full: shed, never block the request
		return
	}
	detached := context.WithoutCancel(ctx)
	r.wg.Go(func() {
		defer func() { <-r.slots }()
		ctx, cancel := context.WithTimeout(detached, 5*time.Second)
		defer cancel()
		if err := r.write(ctx, ev); err != nil {
			r.log.ErrorContext(ctx, "audit write", "err", err)
		}
	})
}

func (r *Recorder) Wait() { r.wg.Wait() } // after the servers stop
```

## singleflight

- `DoChan` + `select` on the caller's ctx, so each caller can leave on its own.
- The shared load runs on `WithTimeout(WithoutCancel(ctx), d)`, so the first caller's cancel doesn't fail the rest.
- `recover()` inside the load: it runs on its own goroutine, where a panic would crash the process.
- Results are shared: read-only, or clone before mutating. The backing cache is bounded.

## Testing

- `goleak.VerifyTestMain(m)` in every package that starts goroutines. Tests close what they open; for an otelhttp-wrapped client, call `CloseIdleConnections()` on the base transport.
- `testing/synctest` (1.25) for timers and retries: fake time, and a goroutine still blocked at the end fails the test.
- CI runs `go test -race -shuffle=on -count=1 ./...`.
- A partial deadlock doesn't crash the process. Find it with `goroutine?debug=1` and the mutex profile (`runtime.SetMutexProfileFraction(5)`).

## Anti-patterns

| Anti-pattern | Fix |
|---|---|
| One goroutine per item, no bound | `SetLimit`, pool, semaphore |
| Receiver closes the channel | Only the sender closes; signal through ctx |
| `make(chan T, len(ch))` | `len` is the current backlog; size from known counts |
| `time.Sleep` to wait for goroutines | Channels, `WaitGroup`, `synctest.Wait` |
| Copying a struct that holds a mutex | Pass pointers (`vet copylocks`) |
| `*gin.Context` / `fiber.Ctx` used in a goroutine | `c.Copy()` or copy the values; they're pooled |
