# Domain modeling playbook

Make illegal states unrepresentable, so the compiler rejects bugs that tests would only catch by luck.

## ❌ → ✅ Quick fixes

| ❌ Avoid | ✅ Prefer | Why |
|---|---|---|
| `isPaid?: boolean; isFailed?: boolean` | discriminated union on `status` | invalid combinations compile |
| `status: string` | a union of string literals | typos compile |
| `enum Status { … }` | `as const` object + derived union | emits runtime code, not erasable |
| `charge(userId: string, orderId: string)` | branded `UserId`, `OrderId` | swapped arguments compile |
| `default: break` in a status switch | `default: return assertNever(s)` | new states silently ignored |
| throwing for an expected business outcome | return a `Result` union | forces the caller to handle it |
| `amount: number` holding float money | `MinorUnits` via a smart constructor | floats, mixed units |
| `order.status = 'paid'` (mutation) | return the new variant object | half-updated invalid state |

## States: discriminated unions, not flags

```ts
// Bad: 2^3 combinations, most invalid (paid && failed? paid without txId?)
type OrderBad = { isPaid?: boolean; isFailed?: boolean; txId?: string; failReason?: string };

type Order =
  | { status: 'pending'; id: OrderId; createdAt: IsoDateTime }
  | { status: 'paid'; id: OrderId; createdAt: IsoDateTime; paidAt: IsoDateTime; txId: string }
  | { status: 'failed'; id: OrderId; createdAt: IsoDateTime; reason: string }
  | { status: 'refunded'; id: OrderId; createdAt: IsoDateTime; refundedAt: IsoDateTime; amount: MinorUnits };

export function orderLabel(o: Order): string {
  switch (o.status) {
    case 'pending': return 'Pending';
    case 'paid': return `Paid ${o.paidAt}`;
    case 'failed': return `Failed: ${o.reason}`;
    case 'refunded': return `Refunded ${o.amount}`;
    default: return assertNever(o); // adding a status breaks the build here
  }
}

export function assertNever(x: never): never {
  throw new Error(`Unhandled variant: ${JSON.stringify(x)}`);
}
```

Transitions as functions that accept only the valid source state: `markPaid(o: Extract<Order, { status: 'pending' }>, txId: string): Extract<Order, { status: 'paid' }>`.

## Branded primitives

```ts
declare const brand: unique symbol;
export type Brand<T, B extends string> = T & { readonly [brand]: B };

export type OrderId = Brand<string, 'OrderId'>;
export type UserId = Brand<string, 'UserId'>;
export type MinorUnits = Brand<number, 'MinorUnits'>; // integer: cents, whole VND…
export type IsoDateTime = Brand<string, 'IsoDateTime'>;

// Smart constructor: the only place a brand is created, and it validates.
export function toMinorUnits(n: number): MinorUnits {
  if (!Number.isSafeInteger(n) || n < 0) throw new RangeError(`invalid amount: ${n}`);
  return n as MinorUnits;
}
```

- Brand where mixing is a real bug (passing a `UserId` as `OrderId`, adding cents to percents). Don't brand everything.
- Brands are type-only (zero runtime cost); the schema at the boundary applies them (e.g. zod `.brand()` or a transform calling the constructor).

## Results for expected failures

```ts
export type Result<T, E> = { ok: true; value: T } | { ok: false; error: E };

type ReserveError = { kind: 'out_of_stock'; sku: string } | { kind: 'sku_not_found'; sku: string };

declare function reserveStock(sku: string, qty: number): Promise<Result<{ reservationId: string }, ReserveError>>;
```

- Use for outcomes the caller must handle (validation, business rules, not-found). The union forces handling.
- Throw for bugs and infrastructure failures (DB down); those go to the error handler.

## Constants and enums

```ts
export const OrderStatus = { Pending: 'pending', Paid: 'paid', Failed: 'failed' } as const;
export type OrderStatus = (typeof OrderStatus)[keyof typeof OrderStatus]; // 'pending' | 'paid' | 'failed'
```

`as const` objects over `enum`: zero runtime surprises, erasable (works with type stripping), and string unions serialize as-is.

## Immutability

- `readonly` fields and `ReadonlyArray<T>` / `readonly T[]` for inputs, config, cached and shared data.
- Updates return new objects (`{ ...o, status: 'paid', ... }`); with unions, construct the full target variant so the compiler checks required fields.

## Other high-value moves

- Template literal types for formatted IDs: `` type Sku = `SKU-${string}` ``.
- Optional vs nullable: `x?: T` means "may be absent", `x: T | null` means "known empty". Pick per field, use `exactOptionalPropertyTypes` to enforce.
- Dates cross boundaries as ISO strings (`IsoDateTime`); convert to `Date`/Temporal inside, explicitly.
- Keep domain types free of ORM/transport types; map at the edges so a DB column rename doesn't ripple everywhere.

## Checklist

- [ ] Lifecycle states are a discriminated union with `assertNever`
- [ ] Easily-confused primitives are branded via smart constructors
- [ ] Expected failures are result unions; bugs throw
- [ ] `as const` instead of `enum`; inputs `readonly`
