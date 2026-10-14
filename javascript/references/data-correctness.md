# Data correctness playbook

Silent wrong answers (a total off by 1 đồng, an ID that changed, a date a day early) cost more than crashes because nobody notices.

## ❌ → ✅ Quick fixes

| ❌ Avoid | ✅ Prefer | Why |
|---|---|---|
| `total * 0.08` for VAT | `applyBp(total, 800)` on integer minor units | float drift |
| `(1.005).toFixed(2)` to round money | integer math + an explicit rounding rule | rounds the binary value → `'1.00'` |
| `parseFloat('19.99') * 100` | parse the decimal string into minor units | `1998.9999…` |
| `new Date('2026-10-03T10:00')` | `'2026-10-03T10:00:00+07:00'` or `…Z` | parsed in server local time |
| `props.items.sort(cmp)` | `props.items.toSorted(cmp)` | mutates the caller's data |
| `JSON.parse(JSON.stringify(o))` | `structuredClone(o)` | drops `Date`/`undefined`/`Map`, throws on `BigInt` |
| `if (!(await exists(k))) await insert(k)` | unique constraint / atomic upsert | race across `await` |
| `Number(row.id)` for `bigint` IDs | keep as string | > 2^53 loses digits |

## Money

- Represent amounts as **integer minor units** (cents; VND and JPY have 0 decimals, KWD has 3) in `Number` while `Number.isSafeInteger` holds, otherwise `BigInt` or a decimal library. Never `0.1 + 0.2` style float math, never `toFixed` for business rounding (it rounds the binary value: `(1.005).toFixed(2) === '1.00'`).
- Store currency with the amount. Format only at the edge with `Intl.NumberFormat`.
- Rates as integers too (basis points: 1% = 100 bp). Make the rounding rule and point explicit (per line vs per invoice, half-up vs half-even); it's a business decision, so state your assumption.

```js
// amount: integer minor units, bp: basis points. Rounds half away from zero.
export function applyBp(amount, bp) {
  if (!Number.isSafeInteger(amount) || !Number.isInteger(bp)) throw new TypeError('integer inputs required');
  if (!Number.isSafeInteger(amount * bp)) throw new RangeError('amount too large, use BigInt');
  const raw = (amount * bp) / 10_000;
  return Math.sign(raw) * Math.round(Math.abs(raw));
}

// Split a total so the parts always sum back exactly (remainder goes to the first parts).
export function allocate(total, parts) {
  const base = Math.floor(total / parts);
  const rest = total - base * parts;
  return Array.from({ length: parts }, (_, i) => base + (i < rest ? 1 : 0));
}
```

- Validate money input as a string pattern (`/^\d+(\.\d{1,2})?$/`) and convert to minor units by string math, not `parseFloat(x) * 100`.
- Discounts never push totals below 0; clamp and test the edge.

## Numbers

| Trap | Safe form |
|---|---|
| `parseInt('08px')` → 8, `parseInt(x)` without radix | `Number(x)` + `Number.isFinite`/`isSafeInteger`; `parseInt(x, 10)` only when trailing text is intended |
| `Number('')` → 0, `Number(null)` → 0 | Reject empty strings before converting |
| `isNaN('abc')` coerces | `Number.isNaN` |
| `x \|\| default` kills valid `0`/`''` | `x ?? default` |
| IDs/int64 > 2^53 (Twitter-style IDs, DB `bigint`) lose digits in JSON/`Number` | Keep as strings end-to-end; `BigInt` for math; `BigInt` isn't JSON-serializable, so convert with `.toString()` |
| Division by zero → `Infinity`, not an error | Guard explicitly |

## Dates & time

- Store and transmit UTC ISO-8601 (`toISOString()`), or epoch ms. Convert to a business timezone (e.g. `Asia/Ho_Chi_Minh`) only for display or day-boundary logic.
- `new Date('2026-10-03')` parses as UTC but `new Date('2026-10-03T10:00')` as local time. Always include `Z` or an offset; never parse locale strings like `'03/10/2026'`.
- Months are 0-based in `new Date(y, m, d)`.
- "Start of day", "end of month", reports, cutoffs: compute in the business timezone (`Intl.DateTimeFormat` with `timeZone`, a tz-aware library, or `Temporal` where available). Adding `24*3600*1000` is wrong across DST.

## Immutability & copying

- Don't mutate inputs: `toSorted`, `toReversed`, `toSpliced`, `with` (ES2023, Node ≥20) instead of `sort`/`reverse`/`splice` on arrays you don't own (props, state, cached values, function args).
- `sort()` without a comparator sorts as strings (`[10, 9, 1].sort()` → `[1, 10, 9]`). Use `(a, b) => a - b`, or `Intl.Collator` / `localeCompare` for human text (Vietnamese diacritics).
- `Object.freeze` config/constants that must never change.

## Concurrency correctness

- Check-then-act across an `await` is a race (`if (!exists) await insert()`, stock decrement, balance update). Use atomic DB operations (`UPDATE ... SET stock = stock - 1 WHERE stock > 0`), unique constraints, transactions with proper isolation, or optimistic locking (version column).
- Make handlers idempotent (dedupe by event/idempotency key); queues and webhooks deliver at least once.

## Equality & coercion

`===` always; compare `Date`s via `getTime()`. Optional chaining (`a?.b`) avoids crashes but doesn't validate.

## Checklist

- [ ] Money in minor units/decimal; rounding rule stated and tested
- [ ] Large IDs as strings; numeric parsing validated
- [ ] Dates UTC in storage, tz-aware for business days
- [ ] No mutation of inputs; comparator on every sort
- [ ] Read-modify-write is atomic; handlers idempotent
