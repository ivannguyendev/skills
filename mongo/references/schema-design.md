# Schema design playbook

Start from the queries, not the entities: list the top 5–10 operations with frequency and latency target, then shape documents so each hot read touches one document or one index range.

## Embed vs reference

| Signal | Choose |
|---|---|
| 1:1 or 1:few (bounded, ≲100 small items), read with parent | Embed |
| 1:many bounded (hundreds), read together | Embed, or subset: newest N embedded, rest in own collection |
| 1:many unbounded (logs, comments, events, history) | Reference: child stores `parentId` (indexed) |
| Child updated often on its own, or shared by many parents | Reference |
| Child has own lifecycle, access rules, or cross-parent queries | Own collection |
| Reads of child need a few parent fields | Extended reference: copy them (`customer: {_id, name}`) |

Duplication is fine when data is read far more than it changes. For every copy decide how it is updated (same write, change stream, batch) and whether staleness is OK. Snapshot data (price at purchase time) is history, not duplication.

## Hard limits

- 16 MB per BSON document. Keep typical docs well under (≲1–2 MB): big docs slow reads, replication and cache.
- Unbounded arrays are the usual path to 16 MB and make updates and multikey indexes expensive. Cap with `$push` + `$slice`, or move items out.
- Files >16 MB: object storage or GridFS + metadata doc.
- A compound index can include at most one array field per document.

## SQL → MongoDB

1. Group tables by aggregate (created, read, deleted together): `orders` + `order_items` → one `orders` doc with `items[]`.
2. Small lookup tables (status, country) → embedded value or enum.
3. Join tables → id array on the bounded side; add extended reference if reads need names.
4. Foreign keys → ids + app-level integrity; transactions only for true cross-document invariants.
5. Auto-increment → `ObjectId`, or natural key + unique index. No counter collection on hot paths.
6. Money → `Decimal128` or integer minor units, never double. Dates → BSON `Date`, UTC.
7. Cut over via dual-write or CDC + reconciliation (counts, checksums) before switching reads.

## Relationships

- **Many-to-many**: ids on the bounded side (`product.categoryIds`, multikey index). Both sides unbounded → link collection `{a, b}` indexed `{a:1,b:1}` and `{b:1,a:1}`.
- **Trees**: `parentId` for simple edits; add `ancestors: [ids]` (indexed) to fetch a subtree in one query; materialized path (`",root,cat,"` + anchored regex) for breadcrumbs. `$graphLookup` is costly; keep off hot paths.

## Patterns (only when the trigger applies)

- **Computed**: reads ≫ writes → maintain totals/counters on write (`$inc`).
- **Bucket**: events per entity per period (1 h or 200 items/doc). Metrics → native time-series collections.
- **Outlier**: few parents with huge child sets → overflow docs + `hasOverflow: true`.
- **Polymorphic**: shapes queried together share a collection with `type`; index `type` + common fields; validator `oneOf`.
- **Attribute**: many sparse similar fields → `attrs: [{k, v}]`, index `{"attrs.k":1,"attrs.v":1}`.
- **Approximation**: non-critical counters → write every N events.
- **Archive**: cold data to archive collection/tier; TTL index for data that just expires.
- **Document versioning**: current in `items`, old versions in `items_history` (same transaction if they must match).
- **Schema versioning**: `schemaVersion` field; code reads all versions, migrates lazily on write or by background batch.

## Validation (`$jsonSchema`)

```js
db.createCollection("orders", {
  validator: { $jsonSchema: {
    bsonType: "object",
    required: ["customerId", "status", "items", "total", "createdAt"],
    properties: {
      customerId: { bsonType: "objectId" },
      status: { enum: ["pending", "paid", "shipped", "cancelled"] },
      items: { bsonType: "array", minItems: 1, maxItems: 200, items: {
        bsonType: "object", required: ["productId", "qty", "price"],
        properties: {
          productId: { bsonType: "objectId" },
          qty: { bsonType: ["int", "long"], minimum: 1 },
          price: { bsonType: "decimal" } } } },
      total: { bsonType: "decimal" },
      createdAt: { bsonType: "date" }
    } } },
  validationLevel: "strict", validationAction: "error"
});
```

- `maxItems` doubles as the unbounded-array guard.
- Existing collection: `collMod` with `validationAction: "warn"` → find violators `db.orders.find({ $nor: [{ $jsonSchema: schema }] })` → fix data → switch to `"error"`. `validationLevel: "moderate"` only as a temporary step.
- Keep validators in version control, applied by migration like DDL.
- Leave `additionalProperties` open unless the domain forbids extra fields; closed schemas break rolling deploys.
