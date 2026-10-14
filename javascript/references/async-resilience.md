# Async & resilience playbook

Every remote call can hang, fail, or succeed twice. Design for all three.

## ❌ → ✅ Quick fixes

| ❌ Avoid | ✅ Prefer | Why |
|---|---|---|
| `Promise.race([call(), timer])` | `fetch(url, { signal: AbortSignal.timeout(5000) })` | race never aborts the call or clears the timer |
| `items.forEach(async (x) => save(x))` | `for (const x of items) await save(x)` or `mapLimit` | forEach drops promises → unhandled rejections |
| `Promise.all(users.map(notify))` | `mapLimit(users, 20, notify)` | unbounded sockets, rate limits |
| `await res.json()` without checking status | check `res.ok`, cancel body on error | error bodies parsed as data |
| `await a(); await b();` (independent) | `await Promise.all([a(), b()])` | latencies add up |
| retrying `POST /charge` blindly | same `Idempotency-Key` on every attempt | a timed-out POST may have succeeded |
| `setTimeout(retry, 2 ** n * 100)` | capped backoff + random jitter | synchronized retry storms |
| `catch (e) {}` / `throw new Error('failed')` | `throw new Error('charge failed', { cause: e })` | root cause lost |

## Timeouts + cancellation

- `fetch` (undici) has no overall timeout. Use `AbortSignal.timeout(ms)`; combine with a caller signal via `AbortSignal.any` (Node ≥20.3).
- Budget timeouts per layer: outer request deadline > sum of inner calls. Pass the request's signal down so a client disconnect cancels downstream work.

```js
export class HttpError extends Error {
  constructor(status, url, retryAfterMs) {
    super(`HTTP ${status} from ${new URL(url).host}`);
    this.name = 'HttpError';
    this.status = status;
    this.retryAfterMs = retryAfterMs;
  }
}

export async function fetchJson(url, { timeoutMs = 5000, signal, ...init } = {}) {
  const timeout = AbortSignal.timeout(timeoutMs);
  const res = await fetch(url, { ...init, signal: signal ? AbortSignal.any([signal, timeout]) : timeout });
  if (!res.ok) {
    await res.body?.cancel();
    const retryAfterMs = Number(res.headers.get('retry-after')) * 1000 || undefined;
    throw new HttpError(res.status, url, retryAfterMs);
  }
  return res.json();
}
```

## Retries

Retry only when the operation is idempotent (GET/PUT/DELETE) or carries an idempotency key that the receiver deduplicates. Reuse the same key across attempts. A timed-out POST may have succeeded.

Retry transient failures only: timeouts, connection resets, 408/425/429/5xx. Never retry 4xx validation/auth errors or user aborts (`AbortError`).

```js
import { setTimeout as sleep } from 'node:timers/promises';

const RETRY_STATUS = new Set([408, 425, 429, 500, 502, 503, 504]);
const RETRY_CODES = new Set(['ECONNRESET', 'ECONNREFUSED', 'ETIMEDOUT', 'EAI_AGAIN', 'UND_ERR_SOCKET', 'UND_ERR_CONNECT_TIMEOUT']);

const isRetryable = (err) =>
  err instanceof HttpError ? RETRY_STATUS.has(err.status)
    : err.name === 'TimeoutError' || RETRY_CODES.has(err.code ?? err.cause?.code);

export async function retry(fn, { attempts = 4, baseMs = 200, maxMs = 10_000, signal } = {}) {
  for (let attempt = 1; ; attempt++) {
    try {
      return await fn(attempt);
    } catch (err) {
      if (attempt >= attempts || signal?.aborted || !isRetryable(err)) throw err;
      const backoff = Math.random() * Math.min(maxMs, baseMs * 2 ** attempt); // full jitter
      await sleep(Math.min(maxMs, err.retryAfterMs ?? backoff), undefined, { signal });
    }
  }
}
```

Jitter prevents synchronized retry storms. Cap total attempts; for many failing calls to one dependency, add a circuit breaker (fail fast while it's down) instead of piling up retries.

## Bounded concurrency

`Promise.all(items.map(call))` on 10k items opens 10k sockets, exhausts the pool and gets you rate-limited. Bound it:

```js
export async function mapLimit(items, limit, fn) {
  const results = new Array(items.length);
  let next = 0;
  const worker = async () => {
    while (next < items.length) {
      const i = next++;
      results[i] = await fn(items[i], i);
    }
  };
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
  return results;
}
```

- When per-item failure is expected, catch inside `fn` and return `{ ok, value | error }` so one failure doesn't hide the rest (the same idea as `Promise.allSettled`).
- Long-running or large batches (minutes, thousands of items, must survive a deploy) belong in a durable job queue with retries and a dead-letter queue, not in-process loops.

## Floating promises

- `items.forEach(async (x) => save(x))`: nothing awaits it, errors become unhandled rejections and the process crashes. Use `for...of` + `await` (sequential) or `mapLimit` (parallel).
- Fire-and-forget only when intended, and always attach a handler: `void audit(evt).catch((err) => log.warn({ err }, 'audit failed'))`.
- In TS, enable lint rules `no-floating-promises` / `no-misused-promises`.

## Errors

- Wrap with context and keep the original: `` throw new Error(`charge ${orderId} failed`, { cause: err }) ``.
- Distinguish expected failures (return a result / typed error) from bugs (throw, let it crash).
- `AggregateError` for multi-failure; log `err.cause` chains.

## Checklist

- [ ] Every outbound call: timeout + signal propagated
- [ ] Retries: transient only, jittered, capped, idempotent
- [ ] Fan-out bounded; big batches in a queue
- [ ] No `forEach(async)`, no un-awaited promises without `.catch`
- [ ] Errors carry `cause` and context ids
