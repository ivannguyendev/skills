---
name: mongo
description: "Production-grade MongoDB: schema design, indexing, query/aggregation performance, connection pooling, security, leak-free driver code, search. Use when designing, writing, reviewing or debugging MongoDB/Mongoose code or data models: SQL/MySQL→MongoDB migration, thiết kế schema, embed vs reference, many-to-many, trees, 16MB limit, slow query / query chậm, COLLSCAN, explain(), đánh index, ESR, tối ưu aggregation, 100% CPU, pagination, lỗi connection, timeout, pool exhaustion, MongoWaitQueueTimeoutError, serverless/Lambda, transactions, $jsonSchema validation, full-text/fuzzy search, tìm kiếm mờ, regex chậm, Atlas Search, vector search. Not for other databases unless migrating to/from MongoDB."
---

# MongoDB Enterprise Playbook

Goal: fast, correct, leak-free MongoDB under production load. Load a `references/` playbook only when the task needs it.

## Hard rules

Apply to every answer and line of code. Justify any exception in one line.

1. **Locality**: data read together is stored together. Embed 1:1 and 1:few; reference only when the child set is unbounded/large or updated independently. Justify every `$lookup` on a hot path.
2. **ESR indexes**: compound keys Equality → Sort → Range. Range before sort forces an in-memory sort.
3. **Zero COLLSCAN in prod**: every prod query shape is indexed and verified with `explain("executionStats")`. Exceptions: tiny collections, offline jobs on a secondary/analytics node.
4. **Sized pool**: never ship default `maxPoolSize` 100 unsized. Serverless: `minPoolSize=0`, `maxPoolSize=3–5`. Long-running: `minPoolSize=10–20`, `maxPoolSize` from load. ~1 MB RAM per connection on mongod.
5. **Strict validation**: critical collections get `$jsonSchema` with `validationLevel: "strict"`, `validationAction: "error"`.
6. **No leaks**: one `MongoClient` per process, created at startup, closed on shutdown; never per request. Exhaust/close cursors, end sessions in `finally`, close change streams.
7. **Bounded everything**: `maxTimeMS`/`timeoutMS` on queries, `limit` on reads, projections over whole docs; no unbounded `toArray()`, no ever-growing arrays.
8. **Secure by default**: TLS, auth, least-privilege app user. Secrets from env/secret manager, never code or logs. Type-check input to block operator injection (`{"$ne": null}`).
9. **Search**: no unanchored or case-insensitive `$regex`, no `$text` on hot paths → Atlas Search / Vector Search (self-managed: mongot). Anchored case-sensitive prefix `/^abc/` is fine (uses index).

## Workflow

1. **Read first**: driver/ODM + version, models, existing indexes, client setup, deployment (serverless/server, Atlas/self-managed).
2. **Ask ≤3 questions**, only what code can't tell: top query patterns, volume/growth, read/write ratio, latency targets. Non-blocking unknowns → state assumption, proceed.
3. **Load one playbook** (max two) from the table.
4. **Answer + verification** proving the fix: e.g. `totalDocsExamined ≈ nReturned`, no `SORT` stage, stable `connections.current`.

## Playbooks

| Task / symptom | Read |
| --- | --- |
| New schema, SQL→MongoDB, embed vs reference, many-to-many, trees, 16MB, polymorphic, versioning, `$jsonSchema` | `references/schema-design.md` |
| Which index, ESR, reading explain, COLLSCAN, building/dropping indexes in prod | `references/indexing.md` |
| Slow query/aggregation, 100% CPU, pagination, bulk writes, profiler | `references/query-performance.md` |
| Client setup, pool sizing, serverless, timeouts, pool exhaustion, `MongoWaitQueueTimeoutError`, ECONNREFUSED | `references/connection-pooling.md` |
| Security, injection, write concern, transactions, leak review, sharding, prod change checklist | `references/production-safety.md` |
| Full-text, fuzzy, autocomplete, slow regex, vector/semantic, hybrid search | `references/search.md` |

Code review: check hard rules first, then open the playbook for each failing area.

## Output

- User's language. Lead with the recommendation or root cause.
- Length: quick question ≤150 words; diagnosis/design ≤400 words + code; review: findings by severity, ~3 lines each (problem, impact, fix).
- Add a section only if it changes the user's next step; offer extras (rollout scripts, monitoring) in one line.
- Code: repo's driver/style, else mongosh + URI options. Production-ready: errors handled, timeouts set, resources released.
- Show the index for every new query shape and the validator for every new critical collection.
