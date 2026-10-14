# Performance playbook

Measure, find the biggest cost, fix it, measure again. Most production slowness is I/O shape (N+1, serial awaits, no batching) or a blocked event loop, not micro-optimizations.

## Measure first

- Event loop health: `perf_hooks.monitorEventLoopDelay()`; alert on p99 delay. High lag = something synchronous is hogging the thread.
- CPU: `node --cpu-prof` (open the `.cpuprofile` in DevTools) or the inspector's profiler; flame graphs for hot paths.
- Latency: p50/p95/p99 per route plus per-dependency timings; an average hides tail latency.
- Benchmark with realistic data sizes; 10 rows hides O(n²).

## Don't block the event loop

One slow synchronous task delays every request on the process.

| Blocker | Fix |
|---|---|
| `fs.*Sync`, `zlib.*Sync`, `crypto.pbkdf2Sync/scryptSync`, sync bcrypt on request path | Async versions (sync is fine at startup only) |
| `JSON.parse`/`stringify` of multi-MB payloads | Body size limit; paginate; stream (NDJSON); move to a worker |
| Regex with nested quantifiers (`(a+)+`, `(.*a){n}`) on user input (ReDoS) | Rewrite without nesting, cap input length, or use a linear-time engine |
| Sorting/processing huge arrays in one go | Chunk with yields, or a worker thread |
| Big CPU work (image, PDF, compression, crypto) | `worker_threads` pool or a job queue |

Yield inside long loops when a worker isn't worth it:

```js
import { setImmediate as yieldToLoop } from 'node:timers/promises';

for (let i = 0; i < rows.length; i++) {
  handle(rows[i]);
  if (i % 1000 === 999) await yieldToLoop();
}
```

## Algorithmic hotspots (Big-O)

| ❌ Avoid | ✅ Prefer | Why |
|---|---|---|
| `users.find((u) => u.id === o.userId)` inside a loop | `const byId = new Map(users.map((u) => [u.id, u]))` once | O(n·m) → O(n+m) |
| `reduce((acc, x) => ({ ...acc, [x.id]: x }), {})` | `new Map(arr.map((x) => [x.id, x]))` | copies every iteration, O(n²) |
| `while (q.length) handle(q.shift())` | index pointer or a real queue | `shift` is O(n) |
| nested loops to dedupe | `[...new Set(arr)]` | O(n²) |
| `arr.sort()` inside a loop, or without comparator | sort once, `(a, b) => a - b` | repeated work; string compare |
| `for (const id of ids) await db.get(id)` | one `WHERE id = ANY($1)` | N+1 round trips |
| several `arr.filter(...).length` counts | one pass with counters | n passes over the data |

## I/O shape

- **N+1**: batch with `WHERE id = ANY($1)` / `IN (...)` or a per-request batching loader.
- **Serial independent awaits** → `Promise.all` (bounded when the list is user-sized).
- Select only needed columns; index filter/sort columns; keyset pagination (`WHERE id > $last ORDER BY id LIMIT n`) instead of large `OFFSET`.
- Reuse connections: one pool per process; HTTP keep-alive (`fetch` pools by default; set `keepAlive: true` on custom `http.Agent`s).
- Cache hot, rarely-changing reads with TTL and a size cap; set an explicit invalidation story.

## Large responses / exports

Never build a 200k-row array then `res.send` it. Stream from a DB cursor through a transform to the response:

```js
import { pipeline } from 'node:stream/promises';
import { Transform } from 'node:stream';

const csvCell = (v) => {
  const s = String(v ?? '');
  const safe = /^[=+\-@\t\r]/.test(s) ? `'${s}` : s; // block CSV formula injection
  return /[",\n]/.test(safe) ? `"${safe.replaceAll('"', '""')}"` : safe;
};

export async function streamCsv(rowStream, res, columns) {
  res.setHeader('content-type', 'text/csv; charset=utf-8');
  res.write(columns.join(',') + '\n');
  const toCsv = new Transform({
    objectMode: true,
    transform(row, _enc, cb) {
      cb(null, columns.map((c) => csvCell(row[c])).join(',') + '\n');
    },
  });
  await pipeline(rowStream, toCsv, res); // backpressure + cleanup on client abort
}
```

`rowStream` is a DB cursor stream (most drivers offer one). Memory stays flat regardless of row count.

## V8-level (only after profiling points here)

- Initialize all object fields in the constructor/literal in the same order (stable shapes); avoid `delete` on hot objects (set `undefined`).
- Avoid polymorphic hot functions that receive many object shapes.
- Prefer typed arrays for large numeric data.

## Frontend quick wins

Debounce input-driven requests and abort stale ones; virtualize long lists; batch DOM reads then writes (avoid layout thrashing); code-split routes; memoize only expensive derived values.

## Checklist

- [ ] Evidence (profile/metric) identifies the bottleneck
- [ ] No sync/CPU-heavy work on request paths
- [ ] No O(n²) over user-scaled data; no N+1
- [ ] Large data streamed, paginated, or bounded
- [ ] Before/after numbers reported
