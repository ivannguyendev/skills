# JS → TS migration playbook

Migrate incrementally, keep the app shippable every day, and never mix type changes with behavior changes in one commit.

## ❌ → ✅ Quick fixes

| ❌ Avoid | ✅ Prefer | Why |
|---|---|---|
| `declare module 'legacy-x';` | minimal typed shim | everything becomes `any` |
| `strict: true` repo-wide in one PR | one flag at a time + error-count ratchet | unreviewable diff |
| rename `.js`→`.ts` and refactor in one PR | type-only PRs, logic changes separate | regressions can't be bisected |
| deleting `typeof` checks because "TS covers it" | keep runtime checks at boundaries | types are erased at runtime |
| `// @ts-ignore` on legacy errors | `// @ts-expect-error TODO(<ticket>)` | tracked, fails once fixed |
| `const o = {}; o.a = 1` | build the object in one literal | implicit `any`, missing props |

## Path

1. **Tooling first**: add `typescript`, a `tsconfig` with `"allowJs": true`, `"checkJs": false`, `"noEmit": true` (or emit matching current output), and `tsc --noEmit` in CI. The build output must stay identical.
2. **Free wins in JS**: `// @ts-check` + JSDoc on critical files catches bugs with no rename:

```js
// @ts-check
/** @typedef {{ id: string, totalMinor: number }} Order */
/** @param {Order[]} orders @returns {number} */
export const sumOrders = (orders) => orders.reduce((s, o) => s + o.totalMinor, 0);
```

3. **Rename leaf modules first** (utils, pure logic, no imports from untyped code), then move upward toward entry points. Each PR: rename + types only, tests green.
4. **Boundaries early**: add runtime schemas where data enters (HTTP, env, queues). That's where JS apps actually break, and it gives typed data to everything downstream.
5. **Turn on strictness** once most files are TS (see ratchet below).

## Strictness ratchet

- Enable flags one at a time (`noImplicitAny` → `strictNullChecks` → rest of `strict` → `noUncheckedIndexedAccess`).
- For a flag with hundreds of errors: record the baseline count, fail CI only if the count rises, burn it down by module. Or keep a stricter `tsconfig` for new/migrated folders and a looser one for legacy.
- Suppress unavoidable legacy errors with `// @ts-expect-error TODO(<ticket>)`, never `@ts-ignore` (it hides future errors silently).

## `any` budget

- Lint `no-explicit-any` as error for new code; legacy `any` tracked and counted.
- Type-aware lint `no-unsafe-assignment`, `no-unsafe-member-access`, `no-unsafe-call`, `no-unsafe-return` catches `any` leaking out of untyped modules, which is where most false confidence comes from.
- Prefer `unknown` + narrowing for genuinely dynamic data.

## Third-party code

- Install `@types/x` when the package doesn't ship types.
- Otherwise write a minimal shim with only what you use:

```ts
// types/legacy-pricing.d.ts
declare module 'legacy-pricing' {
  export function quote(sku: string, qty: number): Promise<{ totalMinor: number }>;
}
```

## Typical traps during migration

- Property bags keyed by user data: `Record<string, T>` (with `noUncheckedIndexedAccess`) or `Map<K, V>`.
- CJS ↔ ESM interop (`module.exports =` vs `export default`): keep the module system unchanged during migration; switch it in a separate project.
- `this` in callbacks and prototypes: convert to classes or closures with explicit types.
- Dynamic `require`/`import` with computed paths: replace with a typed registry map.
- Runtime type checks (`typeof x === 'string'`) are still needed at boundaries. Don't delete them because "TS checks it now".

## Async safety while you're there

Enable `no-floating-promises` and `no-misused-promises` early. Migrations routinely surface `forEach(async)` and un-awaited calls that were silently failing in production.

## Measuring progress

Track per week: % files TS, `any` count, `@ts-expect-error` count, strict-flag errors remaining. Stop when the remaining legacy area isn't changing anymore. Migrating dead code has no ROI.

## Checklist

- [ ] CI type-checks from day one; build output unchanged
- [ ] Leaves first; type-only commits separate from logic changes
- [ ] Boundaries get runtime schemas early
- [ ] Strict flags enabled via a ratchet, not a big bang
- [ ] `any` and suppressions counted and trending down
