---
name: javascript
description: Production-grade JavaScript and Node.js (server and browser) that is fast, leak-free and hard to break in production. Use whenever writing, refactoring or reviewing JS/Node code meant to ship - services, APIs, workers, SDK clients, frontend logic - including the runtime behavior of TypeScript code, and whenever the user mentions memory leaks, RAM growth, slow endpoints, event-loop lag, timeouts, retries, race conditions, money/number bugs, security hardening, graceful shutdown, pre-release review or "enterprise"/"best practice" JS, even if they don't name this skill.
---

# Enterprise JavaScript

Goal: code that survives production - bounded in time, memory and concurrency, correct on bad input, and debuggable at 3 a.m. Reply in the user's language.

## Workflow

1. **Detect context first** (from `package.json`, lockfile, `tsconfig`, existing code): runtime + version (`engines`, `.nvmrc`), ESM vs CJS, framework, test runner, existing libs for HTTP/validation/logging. Reuse what the repo already has; don't add a dependency for something the platform does (`fetch`, `AbortSignal.timeout`, `structuredClone`, `crypto.randomUUID`).
2. **Pick playbooks** from the table below and read only those that match the task. Most tasks need 1-2.
3. **Write the code**, then **self-check** it against the core rules. Fix violations before replying rather than listing them as caveats.
4. In plain JS, catch type bugs cheaply where it pays off: `// @ts-check` + JSDoc on critical modules, runtime schema validation at boundaries.
5. If a rule can't be met (e.g. old Node version, legacy API), say so in one line and use the closest safe alternative.

## Core rules (always on)

**Time & concurrency**

- Every network/DB/queue call has a timeout and is cancellable (`AbortSignal`). `fetch` has no default timeout.
- No floating promises: every promise is awaited, returned, or explicitly handled. Never `array.forEach(async ...)`.
- Fan-out is bounded: no `Promise.all` over unbounded/user-sized arrays; use a concurrency limit.
- Retries only for idempotent or idempotency-keyed operations, with capped exponential backoff + jitter.

**Memory & resources**

- Every `on`/`addEventListener`/`setInterval`/subscription/observer has a matching dispose on the same lifecycle.
- Every in-memory cache/map/queue has a max size or TTL. Module-level `Map` that grows per request = leak.
- Streams go through `pipeline()` (backpressure + cleanup), never manual `.on('data')` + `.write()`.

**Event loop & performance**

- Never block the loop on request paths: no sync crypto/fs/zlib, no `JSON.parse` of unbounded payloads, no regex with nested quantifiers on user input. CPU-heavy work → worker threads or a job queue.
- Avoid O(n²) on collections that scale with users/data (`find`/`includes` inside loops → `Map`/`Set`). No N+1 queries.
- Measure before optimizing; don't trade readability for micro-gains.

**Correctness**

- Validate untrusted input at the boundary (schema), then trust typed data inside.
- Money as integer minor units or a decimal type, never float math. IDs > 2^53 stay strings/`BigInt`.
- Immutable by default: `toSorted`/spread over in-place mutation of inputs.
- Dates stored/transmitted as UTC ISO-8601; never parse ambiguous date strings.

**Errors & security**

- Never swallow errors. Wrap with context: `new Error('charge failed', { cause: err })`. Throw `Error` objects only.
- Clients see a generic message + request id; logs get the stack and context, never secrets/tokens/PII.
- Parameterized queries; no `eval`/`new Function`/`innerHTML` with dynamic data; guard object merges against `__proto__`.
- Unknown state after `uncaughtException`/`unhandledRejection` → log, flush, exit non-zero; let the supervisor restart.

## Playbooks

| File                             | Read when                                                                           |
| -------------------------------- | ----------------------------------------------------------------------------------- |
| `references/async-resilience.md` | HTTP clients, external APIs, retries, timeouts, batching, queues, webhooks          |
| `references/memory-leaks.md`     | RAM grows over time, caches, listeners, timers, streams, SPA/component cleanup      |
| `references/performance.md`      | slow endpoint, event-loop lag, CPU spikes, large data, exports, Big-O hotspots      |
| `references/security.md`         | auth, user input, HTML rendering, crypto, tokens, secrets, dependency risk          |
| `references/data-correctness.md` | money, prices, tax, large IDs, numbers parsing, dates/timezones, cloning            |
| `references/node-service-ops.md` | building/reviewing a Node service: startup config, shutdown, health, logging, pools |

## Output

- **Code task**: the code, then ≤120 words on non-obvious decisions (why this timeout, why this limit). Include tests for the failure paths you guarded (timeout, retry exhaustion, bad input) when the repo has a test runner.
- **Review / audit**: findings ranked by severity (production impact first), each ≤2 lines: `file:line` - problem - fix. Minor issues share one line. Skip sections that wouldn't change what the user does next.
- **Prod incident** (leak, lag, crash): likely causes ranked (review format), ≤5 lines on how to confirm with evidence (metric, heap snapshot, profile), then the fix.
- Reviews and incidents: ≤350 words outside code; code only for the top 1-3 fixes, as minimal diffs or short snippets, not a full rewrite. A full patched file is a one-line offer.
- Offer deeper detail in one line instead of including it.
