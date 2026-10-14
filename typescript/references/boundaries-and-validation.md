# Boundaries & validation playbook

The compiler trusts every type annotation. At process boundaries nothing guarantees the data matches, so annotations there are unchecked claims. Parse, don't cast.

## ❌ → ✅ Quick fixes

| ❌ Avoid | ✅ Prefer | Why |
|---|---|---|
| `const dto = req.body as CreateOrderDto` | `const dto = CreateOrderDto.parse(req.body)` | `as` is an unchecked claim |
| `(await res.json()) as User` | `User.parse(await res.json())` | API drift fails loudly at the edge |
| `x as unknown as T` | fix the types, or parse | hides a real mismatch |
| `user!.id` | narrow: `if (!user) return notFound()` | crashes on real nulls |
| `catch (e: any) { log(e.message) }` | `catch (e) { log(toError(e).message) }` | thrown values aren't always `Error` |
| hand-written `interface` beside a schema | `type Dto = z.infer<typeof Dto>` | twins drift apart |
| `Number(process.env.PORT)` | schema-parse env once at boot | `NaN` at runtime |

## The pattern

`unknown` → runtime schema → inferred type → domain code that never re-validates.

```ts
import { z } from 'zod'; // or the schema lib the repo already uses (valibot, ajv + json-schema-to-ts, …)

export const CreateOrderBody = z.object({
  customerId: z.string().uuid(),
  items: z.array(z.object({
    sku: z.string().min(1).max(64),
    qty: z.number().int().positive().max(1000),
  })).min(1).max(100),
  note: z.string().max(500).optional(),
}).strict(); // reject unknown keys (mass assignment)

export type CreateOrderBody = z.infer<typeof CreateOrderBody>;

app.post('/orders', async (req, res) => {
  const parsed = CreateOrderBody.safeParse(req.body);
  if (!parsed.success) return res.status(400).json({ error: 'invalid_body', issues: parsed.error.issues });
  const order = await createOrder(parsed.data); // fully typed, no casts
  return res.status(201).json(order);
});
```

Schema and type can't drift because the type is derived from the schema.

## Every boundary is `unknown`

| Source | Trap | Do |
|---|---|---|
| `req.body` / query / params | framework types it `any` | schema parse |
| `res.json()` from `fetch` | returns `Promise<any>` | `const data: unknown = await res.json(); return Schema.parse(data);` |
| `JSON.parse` | returns `any` | wrap: `const parseJson = (s: string): unknown => JSON.parse(s);` |
| `process.env` | `string \| undefined`, wrong types | parse once at boot into a frozen config |
| Queue/webhook messages, cache, localStorage | old versions of the shape | schema with versioning/defaults |
| DB rows | usually typed by ORM/codegen | trust generated types; never hand-copy |

```ts
const Env = z.object({
  NODE_ENV: z.enum(['development', 'test', 'production']),
  PORT: z.coerce.number().int().min(1).max(65535).default(3000),
  DATABASE_URL: z.string().url(),
});
export const config = Object.freeze(Env.parse(process.env)); // crash at boot, not at 3 a.m.
```

## Casts and assertions

- `x!`: only when an invariant guarantees it (and say which). With `noUncheckedIndexedAccess`, handle `arr[i]` being `undefined` instead of `!`.
- User-defined guards (`x is T`) and assertion functions (`asserts x is T`) are unchecked by the compiler; keep them tiny and tested, or build them from the schema (`Schema.safeParse(x).success`).

## Errors

```ts
export type AppErrorCode = 'NOT_FOUND' | 'CONFLICT' | 'VALIDATION' | 'UPSTREAM';

export class AppError extends Error {
  readonly code: AppErrorCode;
  constructor(message: string, code: AppErrorCode, options?: { cause?: unknown }) {
    super(message, options);
    this.name = 'AppError';
    this.code = code; // plain field, not a parameter property: stays erasable for type stripping
  }
}

export const toError = (err: unknown): Error =>
  err instanceof Error ? err : new Error(typeof err === 'string' ? err : JSON.stringify(err));
```

- `catch (err)` is `unknown`; narrow with `instanceof` or `toError`.
- Map `AppError.code` to HTTP status in one place; unknown errors → 500 + request id, details only in logs.
- Keep the chain: `throw new AppError('payment failed', 'UPSTREAM', { cause: err })`.

## Contracts between services / frontend

- Single source of truth: shared schema package, or generated types from OpenAPI/GraphQL/protobuf. Regenerate in CI and fail on drift.
- Version breaking changes; additive changes need optional fields and tolerant readers.
- Response types also validated at the consumer when the producer is outside your deploy unit.

## Checklist

- [ ] Every boundary parses `unknown` with a schema
- [ ] Types derived from schemas or codegen, not hand-written twins
- [ ] No `as`/`!` on external data; guards are tested
- [ ] `catch` narrows `unknown`; errors keep `cause`
- [ ] Env parsed and frozen at boot
