# Query & aggregation performance playbook

## Triage: "slow" or "100% CPU"

1. **Find the culprits; don't guess.**
   - Atlas: Query Profiler, Performance Advisor, Query Insights.
   - Self-managed: slow query log (`slowms`, default 100 ms), or briefly `db.setProfilingLevel(1, {slowms: 100, sampleRate: 0.5})` then `db.system.profile.find().sort({millis: -1}).limit(20)`; turn it off after.
   - Live: `db.currentOp({active: true, secs_running: {$gt: 5}})`; stop runaways with `db.killOp(opid)`.
   - Rank query shapes by `count × avg ms`, not the single slowest query.
2. **Explain** the top shapes: COLLSCAN, `SORT` stage, examined/returned ratio.
3. **Classify:**

| Symptom | Likely cause | Fix |
|---|---|---|
| COLLSCAN, high docsExamined | missing or wrong index | ESR index |
| `SORT` stage, high CPU | sort not covered by index | sort keys right after equalities |
| Plan flips between runs | near-duplicate indexes compete | remove redundant ones; `hint` as last resort |
| Many fast queries, CPU still high | N+1 loops in app | batch with `$in`, embed, or one aggregation |
| Slow writes | many indexes, huge docs, growing arrays | trim indexes, reshape docs |
| High disk reads, cache eviction | working set > RAM | projection, smaller docs, archive cold data, scale |
| Ticket/lock waits | long transactions, huge multi-updates | batch writes, shorten transactions |

4. **Verify** with the same explain plus p95/p99 latency, not averages.

## Query hygiene

- Project only needed fields. Paginate by keyset with an `_id` tiebreaker, never `skip` (below).
- `maxTimeMS`/`timeoutMS` on user-facing queries so a bad plan fails fast instead of piling up connections.
- Keep `$in` lists to hundreds; chunk larger sets.
- No `$where`, `$function`, `$accumulator` on hot paths (slow, and a security risk).
- `$ne`, `$nin`, `$not`, `$exists: false` scan widely; model positive states (`status: "active"`).
- `countDocuments` needs an index; for page totals use `estimatedDocumentCount()` or cap ("1000+").

## Pagination

`skip(n)` reads and discards n docs, so every page gets slower. Use keyset pagination with a unique tiebreaker:

```js
// index: { tenantId: 1, createdAt: -1, _id: -1 }
db.orders.find({ tenantId, $or: [
  { createdAt: { $lt: last.createdAt } },
  { createdAt: last.createdAt, _id: { $lt: last._id } } ] })
  .sort({ createdAt: -1, _id: -1 }).limit(50)
```

## Aggregation

- `$match` (and a following `$sort`) at the start, where indexes apply; then `$project`/`$unset` to shrink docs; then `$group`/`$lookup`.
- Adjacent `$sort` + `$limit` → top-k sort with little memory.
- `$lookup`: index `foreignField`, filter both sides first. Many lookups on a hot path → revisit the schema.
- Blocking stages (`$group`, `$sort`, `$bucket`) have a 100 MB memory limit per stage and spill to disk beyond it (default since 6.0), which is slow. Shrink input instead.
- `$facet` sub-pipelines can't use indexes; narrow with `$match` before it.
- `$unwind` multiplies docs; filter and project before it.
- Heavy recurring analytics → precompute into a summary collection with scheduled `$merge`, or run on an analytics/secondary node via `readPreference` tags to protect the primary.

## Writes

- `bulkWrite`/`insertMany` with `ordered: false`, batches of ~500–1000, instead of per-doc round trips.
- Update with operators (`$set`, `$inc`, `$push` + `$slice`), not read-modify-replace: atomic and smaller on the wire and oplog.
- Big `updateMany`/`deleteMany` in prod: batch by `_id` range with pauses to limit replication lag; routine expiry → TTL.
- Upserts need a unique index on the match key, or concurrent upserts create duplicates.
