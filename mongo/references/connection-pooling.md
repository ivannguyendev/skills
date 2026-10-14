# Connection pooling playbook

## Client lifecycle (prevents most leaks and outages)

- One `MongoClient` per process per cluster, created at startup and shared (module singleton or DI). It is thread-safe and owns the pool.
- Never create a client per request, job or function call: each opens its own pool plus monitoring connections → connection storms, memory growth, "too many connections".
- Close once on shutdown: stop accepting work → drain in-flight requests → `client.close()`.
- Fail fast: `ping` at startup before serving traffic; reuse a short-timeout ping for readiness probes.
- Dev hot reload and tests: cache the client on a global so reloads don't open new pools.
- Always set `appName` so connections and slow ops can be traced to a service.

## Sizing

- `maxPoolSize ≈ peak ops/s × avg op seconds × 1.1–1.2` (Little's law). Example: 2,000 ops/s × 0.01 s × 1.2 ≈ 24.
- Unknown load: start at 10–20, watch checkout wait time and failures, adjust. Fixing slow queries shrinks the needed pool more than raising the limit ever helps.
- Server budget (per member that receives app traffic; primary-only reads → only the primary; others get 2 monitoring connections per client): `instances × (maxPoolSize + 2)` must stay below the server connection limit (Atlas tier limit or `net.maxIncomingConnections`), with headroom for autoscaling and deploy overlap (old + new pods). About 1 MB RAM per connection on mongod. Example: 40 pods × (25 + 2) = 1,080 connections on the primary ≈ 1 GB.
- Sharded clusters: size against mongos; mongos manages shard connections.

| Setting | Serverless (Lambda, Cloud Functions, Vercel) | Long-running API / worker |
|---|---|---|
| `maxPoolSize` | 3–5 | formula (typically 20–100) |
| `minPoolSize` | 0 | 10–20 (pre-warmed) |
| `maxIdleTimeMS` | 10000–30000 | 300000–600000 |
| `serverSelectionTimeoutMS` | 5000 | 5000–10000 |
| `connectTimeoutMS` | 5000–10000 | 10000 |
| Wait-queue / op timeout | below function timeout | below request timeout |

Operation timeouts: use `timeoutMS` (client-side operation timeout) as one end-to-end budget when the driver version supports it; it supersedes `maxTimeMS`/`socketTimeoutMS`/`waitQueueTimeoutMS`, so don't mix them; otherwise `maxTimeMS` per query plus `socketTimeoutMS` well above the slowest legitimate query. Never leave all timeouts infinite: a stuck op holds a pooled connection forever.

Serverless: create the client at module scope (outside the handler) so warm invocations reuse it; don't close it per invocation. If you cache the connect promise, reset it when it rejects, or one failed connect breaks the instance for its whole life. Every instance has its own pool, so cap function concurrency (e.g. Lambda reserved concurrency) to fit the server budget.

URI form (works in every driver; secrets injected from the secret manager, password URL-encoded):

```
mongodb+srv://<user>:<pass>@cluster/db?appName=orders-api&maxPoolSize=25&minPoolSize=10&maxIdleTimeMS=300000&serverSelectionTimeoutMS=5000&connectTimeoutMS=10000&retryWrites=true&w=majority
```

Node singleton:

```js
// db.js — imported everywhere, never re-created
import { MongoClient } from "mongodb";
export const client = new MongoClient(process.env.MONGODB_URI);
export const ready = client.connect().then(() => client.db("admin").command({ ping: 1 }));
// startup: `await ready` before listening; crash on failure instead of serving errors
// on shutdown, after the HTTP server drains: await client.close();
```

## Error triage

| Error | Meaning | Fix |
|---|---|---|
| `MongoWaitQueueTimeoutError` / `WaitQueueTimeoutError` / checkout timeout | every pooled connection busy | find slow ops first; check for leaked cursors/sessions; then raise `maxPoolSize` within the server budget |
| `MongoServerSelectionError` / `ServerSelectionTimeout` | no reachable eligible member | IP allowlist, DNS SRV, TLS cert, replica set name, election in progress, read preference with no eligible member |
| `ECONNREFUSED` / `ECONNRESET` | server not listening or dropped | host/port, firewall, server overloaded or restarting; read server logs |
| Connection count keeps climbing | client per request, or never closed | singleton; count by `appName` (below) |
| Latency spikes after idle | pool shrank; reconnect + TLS handshake | raise `minPoolSize`/`maxIdleTimeMS` (not serverless) |
| `NotWritablePrimary` during failover | election (seconds) | keep retryable reads/writes on, idempotent ops, timeouts > election time |

Connections per app (counts the member you run it on; run per member, or via mongos):

```js
db.getSiblingDB("admin").aggregate([
  { $currentOp: { allUsers: true, idleConnections: true } },
  { $group: { _id: "$appName", n: { $sum: 1 } } }, { $sort: { n: -1 } }])
```

## Monitor

- Server: `db.serverStatus().connections`: `current`, `available`, and the growth rate of `totalCreated` (churn).
- Driver pool events: alert on `connectionCheckOutFailed` > 0, rising checkout wait time, and a high `connectionCreated` rate.
