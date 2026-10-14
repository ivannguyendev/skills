# Node service operations playbook

Production-ready = starts safely, reports health honestly, is traceable, stops without dropping work.

## ❌ → ✅ Quick fixes

| ❌ Avoid | ✅ Prefer | Why |
|---|---|---|
| `unhandledRejection` handler that logs and keeps running | log, flush, `process.exit(1)` | state is unknown after a crash |
| `new Pool()` inside a handler | one pool per process, created at startup | connection leak, DB exhausted |
| `process.env.X` read everywhere | validated, frozen `config` at boot | fail fast, one source |
| `console.log` on hot paths | structured logger with redaction | sync I/O, secrets in logs |
| default `keepAliveTimeout` (5s) behind an LB | `65_000` + `headersTimeout` `66_000` | random 502s |
| `res.json(a)` then later `res.json(b)` | `return res.json(a)` | `ERR_HTTP_HEADERS_SENT` |
| liveness probe that checks the DB | `/livez` cheap, `/readyz` checks deps | a DB blip restarts every pod |
| `npm start` as container command | `node dist/index.js` (or `tini`) | SIGTERM may not reach node |

## Config at boot

- Parse and validate all env vars once at startup with a schema (types, required, ranges); export a frozen config object. Fail fast with a clear message listing missing keys (never their values).
- No defaults for secrets.

## Graceful shutdown

Orchestrators send SIGTERM then SIGKILL after a grace period (Kubernetes default 30s). Stop accepting, finish in-flight work, close resources, exit before the deadline.

```js
let shuttingDown = false;

export function installShutdown({ server, logger, resources, graceMs = 25_000 }) {
  const shutdown = async (signal) => {
    if (shuttingDown) return;
    shuttingDown = true; // readiness probe now returns 503 so the LB drains traffic
    logger.info({ signal }, 'shutdown started');
    const force = setTimeout(() => {
      logger.error('shutdown timed out, forcing exit');
      server.closeAllConnections();
      process.exit(1);
    }, graceMs).unref();
    try {
      await new Promise((resolve) => server.close(resolve)); // stop accepting; waits for in-flight
      await Promise.allSettled(resources.map((r) => r.close())); // db pools, queue consumers, cache clients
      clearTimeout(force);
      process.exit(0);
    } catch (err) {
      logger.error({ err }, 'shutdown failed');
      process.exit(1);
    }
  };
  process.once('SIGTERM', shutdown);
  process.once('SIGINT', shutdown);
}

export const isShuttingDown = () => shuttingDown;
```

- Queue consumers: stop pulling new messages first, finish/ack the current ones, then close.
- Intervals/cron inside the process: clear them in the same shutdown path.

## Crash policy

- After `uncaughtException` the process state is unknown: log (synchronously or flush), then `process.exit(1)` and let the supervisor restart. Never "log and continue".
- Node ≥15 already crashes on unhandled rejections. Add a handler only to log with context, then exit.

## Health

`/livez` has no dependency checks; `/readyz` checks deps (short timeouts) and returns `503` while starting or shutting down.

## HTTP server settings

```js
server.keepAliveTimeout = 65_000; // > load balancer idle timeout, avoids random 502s
server.headersTimeout = 66_000;   // must exceed keepAliveTimeout
server.requestTimeout = 30_000;   // cap slow clients
```

- Body size limits on JSON/urlencoded parsers.
- Error middleware: if `res.headersSent`, `return next(err)`.
- Express 4 doesn't catch rejected async handlers; wrap them or use Express ≥5 / a framework that does.

## Logging & tracing

- Structured JSON logs (e.g. pino) with levels; no `console.log` on hot paths (synchronous for files and pipes on Linux).
- Request id on every log line via `AsyncLocalStorage`, with no parameter threading:

```js
import { AsyncLocalStorage } from 'node:async_hooks';
import { randomUUID } from 'node:crypto';

export const requestContext = new AsyncLocalStorage();

export function requestIdMiddleware(req, res, next) {
  const incoming = req.headers['x-request-id'];
  const requestId = typeof incoming === 'string' && /^[\w-]{8,64}$/.test(incoming) ? incoming : randomUUID();
  res.setHeader('x-request-id', requestId);
  requestContext.run({ requestId }, next);
}

export const logContext = () => requestContext.getStore() ?? {};
// logger.info({ ...logContext(), orderId }, 'order placed')
```

- Redact secrets/PII at the logger level. Log errors as objects (`{ err }`) so stack and `cause` are kept.
- Distributed tracing (e.g. OpenTelemetry) for multi-service calls; propagate trace headers on outbound requests.

## Metrics worth alerting on

Event-loop delay p99, heap used vs limit, RSS, GC pauses, HTTP latency p95/p99 and 5xx rate per route, DB pool wait time and saturation, outbound dependency errors/timeouts, queue lag.

## Resources

- One DB pool per process, created at startup. Size it so `instances × poolMax < db max_connections` with headroom; set acquire timeout and statement timeout; always release clients in `finally`.
- Reuse HTTP clients/agents (keep-alive). Per-request clients leak sockets.
- Memory: `--max-old-space-size` ≈ 75% of the container limit; scale horizontally (instances) rather than one giant process. CPU-heavy work goes to workers.

## Checklist

- [ ] Config validated at boot, frozen
- [ ] SIGTERM drains server, consumers, pools within the grace period
- [ ] Crash on unknown state; supervisor restarts
- [ ] Liveness vs readiness separated; readiness flips on shutdown
- [ ] Server timeouts set; one response per request
- [ ] Structured logs with request id; secrets redacted; key metrics exported
