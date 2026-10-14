# Indexing playbook

## ESR order

One compound index per important query shape (filter + sort + projection):

1. **Equality** fields first (`tenantId: X`, `status: "active"`).
2. **Sort** fields next, in sort order. Direction matters only for multi-field sorts: `{a:1,b:-1}` serves `sort({a:1,b:-1})` and its reverse, not `sort({a:1,b:1})`.
3. **Range** fields last (`$gt/$lt`, `$ne`, `$nin`, unanchored regex).

- `find({tenantId, status, createdAt: {$gte: d}}).sort({priority: -1})` → `{tenantId:1, status:1, priority:-1, createdAt:1}`.
- Range and sort on the same field → one key: `{tenantId:1, status:1, createdAt:-1}`.
- `$in` filters like equality, but with a sort it may need SORT_MERGE or an in-memory sort; check explain.
- Prefix rule: `{a:1,b:1,c:1}` serves `a`, `a,b`, `a,b,c`, not `b` or `c` alone. `{a:1}` is redundant when `{a:1,b:1}` exists, unless it is unique, TTL, partial, or has a collation.

## Reading explain

`db.c.find(q).sort(s).explain("executionStats")`, same for `.aggregate(p)`:

| Signal | Healthy | Problem |
|---|---|---|
| Winning plan | `IXSCAN` → `FETCH` | `COLLSCAN` |
| `SORT` stage | absent | in-memory sort: CPU, spills to disk past 100 MB (errors on ≤5.x) → fix ESR order |
| `totalKeysExamined / nReturned` | ≈ 1 | ≫ 1: wrong key order or low selectivity |
| `totalDocsExamined / nReturned` | ≈ 1 (0 if covered) | ≫ 1: filter field not in index |

Test on production-sized data; plans on small dev data mislead.

## Index types

- **Unique**: business keys, e.g. `{tenantId:1, email:1}, {unique: true}`. Enforce in the DB, never check-then-insert.
- **Partial**: `partialFilterExpression: {status: "active"}`; the query must include that filter. Prefer over sparse.
- **TTL**: `{expireAt:1}, {expireAfterSeconds: 0}` for sessions, tokens, temp data; the deleter runs ~every 60 s.
- **Covered**: index holds all filter + projected fields, `_id` excluded → `totalDocsExamined: 0`.
- **Multikey**: array fields; large arrays mean large indexes.
- **Wildcard `$**`**: only for truly unpredictable attribute queries.
- **Collation**: case-insensitive equality → index with `{locale: "en", strength: 2}` and query with the same collation, instead of `/^x$/i`.

## Index budget

Each index costs RAM and write latency. Aim for ≲10 per collection.

- Usage: `db.c.aggregate([{ $indexStats: {} }])`; low `accesses.ops` over a long uptime = candidate.
- Drop safely: `hideIndex(name)` → watch a full business cycle → `dropIndex`, or `unhideIndex` to revert instantly.
- Redundant: a prefix of another index, or same keys with different options.

## Index changes in production

- Builds (4.2+) lock only briefly at start and end but cost CPU, IO and disk. Run off-peak; very large collections → rolling build one member at a time (Atlas has an option).
- Ship indexes through migrations or IaC, reviewed like schema. Disable ODM auto-indexing in prod (Mongoose `autoIndex: false`).
- Create the index before deploying code that depends on it.
- Enforce Zero COLLSCAN in CI: run integration tests against mongod with `--setParameter notablescan=1` so unindexed queries fail there (it also rejects unfiltered `find({})`). Never enable on prod.
