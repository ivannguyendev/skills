# Search playbook

## Pick the tool

| Need | Use |
|---|---|
| Exact match, or prefix of a code/SKU/path, case-sensitive | B-tree index + equality or anchored regex `/^ABC-12/` (bounded index scan) |
| Case-insensitive exact match | index with collation `strength: 2`, query with the same collation |
| Words, relevance, typos, autocomplete, facets, synonyms, accents, highlighting | Atlas Search `$search` |
| Meaning, not exact words; RAG; recommendations | Vector Search `$vectorSearch` |
| Relevance + meaning | Hybrid: `$rankFusion` (8.0+) or `$scoreFusion` (8.2+), verify for your deployment; older → run both, merge with reciprocal rank fusion in app |

Not allowed on hot paths:

- Unanchored (`/phone/`) or case-insensitive (`/^phone/i`) `$regex`: scans the whole index or collection; cost grows with data.
- `$text`: one per collection, no fuzzy or autocomplete, weak relevance and language support. Acceptable only for low-traffic admin tools with no search engine available.

Self-managed (no Atlas): `$search`/`$vectorSearch` run via `mongot` on MongoDB 8.2+ (public preview at launch; check current status). Otherwise use a dedicated engine (Elasticsearch/OpenSearch) fed by change streams.

## Atlas Search (lexical)

Static mappings: dynamic mappings index every field and bloat the index.

```json
{ "mappings": { "dynamic": false, "fields": {
  "name": [
    { "type": "string", "analyzer": "lucene.standard" },
    { "type": "autocomplete", "tokenization": "edgeGram", "minGrams": 2, "maxGrams": 15, "foldDiacritics": true } ],
  "brand":    { "type": "token" },
  "tenantId": { "type": "token" },
  "price":    { "type": "number" } } } }
```

```js
db.products.aggregate([
  { $search: { index: "products", compound: {
      must:   [{ text: { query: q, path: "name", fuzzy: { maxEdits: 1, prefixLength: 2 } } }],
      filter: [{ equals: { path: "tenantId", value: tenantId } }] } } },
  { $limit: 20 },
  { $project: { name: 1, price: 1, score: { $meta: "searchScore" } } }
])
```

- `$search` must be the first stage. Put filters in `compound.filter`, not a later `$match`, which would run over every hit.
- Fuzzy: `maxEdits: 1` with `prefixLength` 1–2 stays fast; `maxEdits: 2` only for long terms.
- Autocomplete: the `autocomplete` operator on the autocomplete-typed field; debounce in the UI.
- Accent-insensitive (Vietnamese etc.): `foldDiacritics` on autocomplete; a custom analyzer with `icuFolding` for text.
- Pagination: `searchAfter` with `$meta: "searchSequenceToken"`, not `$skip`.
- Counts and facets: `$searchMeta`; `count: { type: "lowerBound" }` for speed.
- The index lags writes (usually seconds); don't use it for read-after-write checks.

## Vector Search (semantic)

```json
{ "fields": [
  { "type": "vector", "path": "embedding", "numDimensions": 1536, "similarity": "cosine" },
  { "type": "filter", "path": "tenantId" },
  { "type": "filter", "path": "lang" } ] }
```

```js
db.docs.aggregate([
  { $vectorSearch: { index: "docs_vec", path: "embedding", queryVector: vec,
      numCandidates: 200, limit: 10, filter: { tenantId, lang: "vi" } } },
  { $project: { title: 1, score: { $meta: "vectorSearchScore" } } }
])
```

- `numCandidates` ≈ 10–20 × `limit`; tune recall against latency on a labeled sample.
- Fields in `filter` must be declared as `filter` in the index (pre-filtering). A `$match` afterwards drops results instead of finding more.
- Same embedding model and dimensions for documents and queries; store the model version and re-embed when it changes.
- Large corpora: enable quantization (`"quantization": "scalar"` or `"binary"`) to cut RAM; exclude vectors from default projections.
- Multi-tenant: always filter by tenant inside `$vectorSearch`.
