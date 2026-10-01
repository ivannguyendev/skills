# Resilience and operations: admission, reconnect storms, deploys, health, SLOs, testing

## The failure loop to design against

Node restart → tens of thousands reconnect at once → auth/session/Redis saturate → nodes slow → health checks fail → orchestrator restarts more nodes → more reconnects. Break every link.

## Admission control on upgrade

Reject cheaply **before** auth and before any remote call.

```js
const bucket = { tokens: 500, rate: 500, at: Date.now() };
function admit() {
  const now = Date.now();
  bucket.tokens = Math.min(bucket.rate, bucket.tokens + ((now - bucket.at) / 1000) * bucket.rate);
  bucket.at = now;
  if (bucket.tokens < 1) return false;
  bucket.tokens -= 1;
  return true;
}

upgrade: (res, req, context) => {
  if (!admit() || draining) {
    return res.writeStatus('503 Service Unavailable').writeHeader('Retry-After', String(5 + Math.floor(Math.random() * 25))).end();
  }
  // read headers, then async auth (see security.md)
},
```

- Separate concurrency limit for the connect path so connects cannot starve existing sockets.
- Fleet-wide new-connection rate at the LB as a second layer.

## Cheap connect

- Verify JWT locally (signature, `exp`, `aud`, `iss`); no synchronous session-service call per connect.
- Revocation: short-lived tokens + a revocation list pushed over pub/sub, or an LRU of session validity with TTL.
- Circuit breaker on any remaining dependency: when open, admit valid tokens and re-check in the background.
- Batch and ref-count broker subscriptions per node (never per socket).
- Resume token + last seq so reconnect rehydrates only the delta.

## Drain and rollout

On SIGTERM:

1. Readiness → failing (LB stops sending new upgrades); keep liveness passing.
2. Stop admitting; wait for LB deregistration.
3. Close sockets **gradually** over minutes (e.g. 40k over 5–10 min) with code **1012 (Service Restart)** and a reconnect-delay hint in the reason or a final control frame.
4. Force-close leftovers at the deadline; close broker clients; exit.

`terminationGracePeriodSeconds` covers the whole drain. Rollout:

- Surge first: `maxSurge ≥ 1`, `maxUnavailable: 0`; the new node is ready before an old one drains.
- Advance only when connect rate, dependency CPU, event-loop lag and delivery p99 are back to baseline; auto-pause on SLO breach.
- Canary one cell → one region → the rest; never two regions at once.
- Protocol changes additive; gateways accept protocol N and N−1.
- Scale-in always through the drain path.

## Health checks

| Check | Means | Must not |
| --- | --- | --- |
| Liveness | Process alive, event loop responsive | Depend on Redis/DB/auth; fail under load |
| Readiness | Accepting new connections now | Restart the pod when failing |

Add a restart budget / remediation circuit breaker: the platform may not restart more than N gateways in M minutes.

## Client side (see `client.md`)

Full-jitter exponential backoff, honor `Retry-After` and 1012/1013 hints, reset backoff only after ~30 s of stable connection. Old clients in the field won't have fixes — server-side admission must protect you alone.

## SLIs, SLOs, alerts

| SLI | Typical SLO / alert |
| --- | --- |
| Connect success rate | ≥ 99.9–99.99 % |
| End-to-end delivery latency (sender gateway receipt → recipient client) | p99 under target; burn-rate alerts |
| Delivered-or-resynced within N s | ≥ 99.99 % |
| Concurrent connections per node / fleet | Capacity alerts at 70 % |
| Connect rate, rejected (503) rate | Spike detection after deploys |
| Close codes (1006 abnormal, 1012, 1013, app codes) | Spike detection |
| Resync / seq-gap rate | The loss indicator |
| Event-loop delay p99, GC pause p99, CPU | Saturation |
| `bufferedAmount` distribution, slow-consumer disconnects | Backpressure health |
| Broker lag / pending, Redis CPU, output buffers | Fan-out health |

- Synthetic clients in every region sending to each other continuously — primary paging signal.
- Multi-window burn-rate alerting (e.g. 1 h/5 m fast, 6 h/30 m slow).
- Trace id in the message envelope through gateway → service → store → broker → recipient gateway; tag metrics by cell/region/node.

## Load and chaos testing — release gate

Tools: k6 (WebSocket module) or Artillery for up to hundreds of thousands of connections; custom Go/Rust clients for millions (multiple source IPs to avoid ephemeral-port limits). Measure latency with timestamps sent and received on the same host.

| Scenario | Pass |
| --- | --- |
| Ramp to 1.5× peak, then multi-hour soak | SLOs hold; no memory/GC drift |
| Mass reconnect: drop the largest node, then 10–30 % of fleet | Recovers within target, no cascading restarts |
| Node kill under load | Clients reconnect, resync, zero lost/duplicated durable messages |
| Slow consumers (throttled 5–10 % of clients) | Bounded memory, policy applied |
| Hot room (max members, max input rate) alongside normal load | Isolation holds |
| Deploy during peak | Drain paced, SLOs hold |
| Dependency loss (Redis shard, broker node, DB replica) | Degrades as designed, recovers |

Pass criteria: every SLO with ≥ 30 % headroom. Then staged production rollout (1 % → 10 % → 50 % → 100 %).
