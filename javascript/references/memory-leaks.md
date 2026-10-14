# Memory & resource leaks playbook

A leak is anything reachable from a long-lived root (module scope, global emitter, timer, pending promise, DOM) that grows with traffic or time.

## Usual suspects (check in this order)

| Pattern | Fix |
|---|---|
| Module-level `Map`/array/object filled per request (cache, "seen" set, metrics) | Cap it: LRU + TTL, or an external cache |
| Listener added per request/component on a long-lived emitter (`bus.on`, `process.on`, `window.addEventListener`, socket) | Remove on the same lifecycle; prefer `{ signal }` / `once` |
| `setInterval`/`setTimeout` never cleared, or created per call | Keep the handle, clear on dispose; `.unref()` background timers |
| Promise that never settles (waiting for an event that never fires) | Add timeout/abort; clean up listeners on every exit path |
| Closure in a long-lived callback capturing `req`/`res`/big buffers | Capture only the fields needed |
| Streams piped manually, not destroyed on error | `await pipeline(...)` |
| Per-request creation of clients/pools/agents | Create once at module init, reuse |
| Browser: observers, intervals, sockets, fetch not aborted on unmount, detached DOM kept in arrays | Cleanup function per effect/component |

`MaxListenersExceededWarning` is a leak signal. Fix the listener lifecycle; don't raise the limit.

## Bounded cache

```js
export class LruCache {
  #map = new Map();
  constructor({ max = 1000, ttlMs = 60_000 } = {}) {
    this.max = max;
    this.ttlMs = ttlMs;
  }
  get(key) {
    const hit = this.#map.get(key);
    if (!hit) return undefined;
    this.#map.delete(key);
    if (hit.expires <= Date.now()) return undefined;
    this.#map.set(key, hit); // move to most-recent
    return hit.value;
  }
  set(key, value) {
    this.#map.delete(key);
    this.#map.set(key, { value, expires: Date.now() + this.ttlMs });
    if (this.#map.size > this.max) this.#map.delete(this.#map.keys().next().value);
  }
}
```

- Prevent stampedes: cache the in-flight promise, delete it on rejection.
- Per-object metadata: `WeakMap` keyed by the object (auto-freed). Don't build logic on `WeakRef`/`FinalizationRegistry`; GC timing is not guaranteed.
- Multi-instance deployments: an in-process cache is per instance; use a shared cache when consistency matters.

## Listener lifecycle (every exit path cleans up)

```js
export function waitForJob(bus, jobId, signal) {
  signal.throwIfAborted();
  return new Promise((resolve, reject) => {
    const cleanup = () => {
      bus.off('done', onDone);
      signal.removeEventListener('abort', onAbort);
    };
    const onDone = (id, result) => {
      if (id !== jobId) return;
      cleanup();
      resolve(result);
    };
    const onAbort = () => {
      cleanup();
      reject(signal.reason);
    };
    bus.on('done', onDone);
    signal.addEventListener('abort', onAbort, { once: true });
  });
}
// caller: await waitForJob(bus, id, AbortSignal.timeout(30_000));
```

For `EventTarget`/DOM, one `AbortController` removes many listeners: `el.addEventListener('scroll', fn, { signal: ac.signal })`, then `ac.abort()`.

## Browser / component cleanup

```js
useEffect(() => {
  const ac = new AbortController();
  const id = setInterval(refresh, 30_000);
  const ro = new ResizeObserver(onResize);
  ro.observe(ref.current);
  fetch(url, { signal: ac.signal }).then(setData).catch((e) => { if (e.name !== 'AbortError') setError(e); });
  return () => { ac.abort(); clearInterval(id); ro.disconnect(); };
}, [url]);
```

Same rule in any framework: whatever an effect/mount starts, its cleanup/unmount stops.

## Streams & handles

- `import { pipeline } from 'node:stream/promises'`: handles backpressure and destroys all streams on error or abort.
- Stop work when the client leaves: `res.on('close', () => ac.abort())`.
- Always `release()` pooled DB clients in `finally`; close file handles (`await using`/`finally`).

## Confirming a leak (evidence before fixing)

1. Trend `process.memoryUsage()`: `heapUsed` growing = JS objects; `rss`/`external`/`arrayBuffers` growing with flat heap = buffers/native.
2. Run with `--heapsnapshot-signal=SIGUSR2` (or the inspector). Take 3 snapshots under steady load (warm-up, +N min, +2N min) and compare: "objects allocated between snapshots", sorted by retained size. Read the retainer path to find the root.
3. Reproduce with a load loop locally; verify the fix flattens the curve.
4. Set `--max-old-space-size` to ~75% of the container memory limit so V8 collects before the container is OOM-killed.

## Checklist

- [ ] No unbounded module-level collections
- [ ] Every subscribe/timer/observer has a dispose on the same lifecycle
- [ ] Pending promises have a timeout/abort path
- [ ] Streams via `pipeline`; client disconnect aborts work
- [ ] Clients/pools are singletons
