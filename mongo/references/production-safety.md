# Production safety playbook

## Security baseline (a line each)

- TLS on every connection; `tlsAllowInvalidCertificates` only in local dev.
- Auth: SCRAM-SHA-256, x.509, or OIDC / cloud workload identity; prefer short-lived credentials.
- App user = least privilege: `readWrite` on its own DB or a custom role. Never `root`/`dbAdmin`/`clusterAdmin` for apps; separate users for migrations and ops.
- Secrets from env or a secret manager, never in code, git, images or logs. Redact the URI in logs and errors.
- Network: private endpoint / VPC peering / IP allowlist; never `0.0.0.0/0` in prod.
- Encryption at rest; Queryable Encryption or CSFLE for regulated fields (PII, cards) when DB admins are in the threat model.
- Audit logging where compliance requires it.
- Self-managed: `security.javascriptEnabled: false` unless needed.

## Operator injection

`{ email: req.body.email }` with body `{"email": {"$ne": null}}` matches any user.

- Validate and type-check input at the boundary (zod, joi, pydantic, class-validator); reject objects where a scalar is expected.
- Or force literal matching: `{ email: { $eq: input } }`. Mongoose: `mongoose.set("sanitizeFilter", true)`.
- Never pass user input into `$where`, `$function`, `$accumulator`, `mapReduce`, or pipeline stages built from raw JSON.
- Whitelist client-supplied sort/projection field names.
- User text in a regex: escape it, anchor it, cap its length.

## Write safety

- `w: "majority"` (default since 5.0, but falls back to `w:1` with arbiters; set it explicitly) for anything that must survive failover; `w: 1` only for disposable data.
- Keep retryable writes/reads on (default). Make writes idempotent (upsert on a unique business key, idempotency key per request) so retries can't double-apply.
- Single-document updates are atomic; model invariants into one document where possible. Read-modify-write → optimistic concurrency: `version` in the filter + `$inc: {version: 1}`, retry on 0 matches.

## Transactions

Only for real multi-document invariants (transfers, order + inventory reservation).

```js
const session = client.startSession();
try {
  await session.withTransaction(async () => {
    const debit = await accounts.updateOne(
      { _id: from, balance: { $gte: amt } }, { $inc: { balance: -amt } }, { session });
    if (debit.modifiedCount !== 1) throw new Error("INSUFFICIENT_FUNDS"); // aborts
    await accounts.updateOne({ _id: to }, { $inc: { balance: amt } }, { session });
  }, { readConcern: { level: "snapshot" }, writeConcern: { w: "majority" } });
} finally {
  await session.endSession();
}
```

- `withTransaction` retries `TransientTransactionError` and `UnknownTransactionCommitResult`; hand-rolled loops usually miss one. Because the callback may run again, keep external side effects (email, HTTP) out; use an outbox.
- Pass `{ session }` to every op inside, or it runs outside the transaction.
- Keep them short (<1 s, <1,000 docs). The 60 s default lifetime is a ceiling, not a target; long transactions pin cache and cause write conflicts.

## Leak checklist (every PR touching data access)

- [ ] One client per process; no `new MongoClient` / `mongoose.connect` in handlers, loops, or per tenant without caching.
- [ ] Cursors fully iterated (`for await`) or `cursor.close()` in `finally` when exiting early.
- [ ] No `noCursorTimeout: true`.
- [ ] No unbounded `.toArray()` / `find({})` into memory; `limit`, or stream in batches.
- [ ] `session.endSession()` in `finally`.
- [ ] Change streams closed on shutdown; resume token persisted; `ChangeStreamHistoryLost` handled by re-sync.
- [ ] Listeners and intervals tied to the client removed; no closures holding large result sets.
- [ ] GridFS/download streams destroyed when the client aborts.

## Sharding (decide early for collections that will outgrow one node)

- Shard key: high cardinality, even write spread, present in most queries (else scatter-gather).
- Not a monotonic key alone (`ObjectId`, timestamp) → hot shard. Use compound (`{tenantId:1, _id:1}`) or hashed.
- Unique indexes must be prefixed by the shard key.
- `refineCollectionShardKey` (4.4+, add suffix fields) is cheap; resharding (5.0+) is expensive. Still choose upfront.

## Operations

- Backups with regularly tested restores (Atlas continuous backup / PITR, or Ops Manager). An untested backup doesn't count.
- Alert on replication lag, oplog window (keep ≥24–72 h), cache usage/dirty %, connections, slow ops, disk.
- Indexes, validators, migrations and upgrades: code review → staging with prod-sized data → off-peak rollout → ready rollback (`hideIndex`, `validationAction: "warn"`, feature flag).
- After a server upgrade, raise `featureCompatibilityVersion` only once the new version is stable.
