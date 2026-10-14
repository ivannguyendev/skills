# Advanced types playbook

Advanced types belong in shared helpers and library boundaries, where one clever type saves many call sites from mistakes. In application code, prefer plain, named types a new teammate can read.

## ❌ → ✅ Quick fixes

| ❌ Avoid | ✅ Prefer | Why |
|---|---|---|
| `function log<T>(x: T): void` | `function log(x: unknown): void` | a generic used once adds nothing |
| `function get<T>(key: string): T` | `get<K extends keyof Config>(key: K): Config[K]` | caller-chosen `T` is a disguised cast |
| `Omit<SomeUnion, 'id'>` | `T extends unknown ? Omit<T, K> : never` | `Omit` collapses unions |
| `cb: Function` | `cb: (e: OrderEvent) => void` | untyped call signature |
| `{}` meaning "any object" | `object` or `Record<string, unknown>` | `{}` accepts primitives |
| `as any` inside a generic implementation | `unknown` + constraint/narrowing | `any` leaks to callers |
| `A & B & { … }` repeated inline | `interface X extends A, B { … }` | slower checker, worse errors |
| `// @ts-ignore` | `// @ts-expect-error <reason>` | ignore hides future errors |

## Generics that earn their keep

- A type parameter must relate two things (input ↔ output, key ↔ value). Used once = unnecessary; replace with the constraint type.
- Constrain: `<T extends { id: string }>`; avoid `<T>` + casts inside.
- `const` type parameters (TS ≥5.0) keep literal types without callers writing `as const`: `function route<const P extends string>(path: P)`.
- `NoInfer<T>` (TS ≥5.4) stops a parameter from widening inference: `function pick<T extends string>(all: T[], fallback: NoInfer<T>)`.
- Return precise types from overloads only when callers really branch on them; otherwise one signature.

## Mapped, conditional, template literal

```ts
// Key remapping: build handler names from events
type Handlers<E> = { [K in keyof E & string as `on${Capitalize<K>}`]: (payload: E[K]) => void };

// Conditional + infer: unwrap
type ElementOf<T> = T extends readonly (infer U)[] ? U : never;

// Distribution: unions distribute over naked T; wrap to stop it
type IsString<T> = [T] extends [string] ? true : false;

// Deep readonly (bounded use: config, cached data)
type DeepReadonly<T> = T extends (...args: never[]) => unknown ? T
  : T extends object ? { readonly [K in keyof T]: DeepReadonly<T[K]> } : T;
```

Know the built-ins before writing your own: `Pick`, `Omit`, `Partial`, `Required`, `Readonly`, `Record`, `Extract`, `Exclude`, `NonNullable`, `ReturnType`, `Parameters`, `Awaited`, `InstanceType`.

`Omit` on a union isn't distributive (`Omit<A | B, 'x'>` loses variant-specific keys). Use `T extends unknown ? Omit<T, K> : never`.

## Example: typed event bus with built-in cleanup

Types prevent wrong payloads; the returned unsubscribe prevents listener leaks.

```ts
type Listener<T> = (payload: T) => void;

export class TypedEmitter<Events extends object> {
  #listeners: { [K in keyof Events]?: Set<Listener<Events[K]>> } = {};

  on<K extends keyof Events>(event: K, fn: Listener<Events[K]>, signal?: AbortSignal): () => void {
    const set = (this.#listeners[event] ??= new Set<Listener<Events[K]>>());
    set.add(fn);
    const off = () => { set.delete(fn); };
    signal?.addEventListener('abort', off, { once: true });
    return off;
  }

  emit<K extends keyof Events>(event: K, payload: Events[K]): void {
    for (const fn of [...(this.#listeners[event] ?? [])]) fn(payload); // snapshot: safe if a listener unsubscribes
  }

  listenerCount(event: keyof Events): number {
    return this.#listeners[event]?.size ?? 0;
  }
}

type AppEvents = { 'order.paid': { orderId: string; amount: number }; 'user.deleted': { userId: string } };
const bus = new TypedEmitter<AppEvents>();
const off = bus.on('order.paid', ({ orderId }) => console.log(orderId));
off();
```

## Type tests

Types that others depend on deserve tests that run in `tsc`:

```ts
// @ts-expect-error payload shape is enforced
bus.emit('order.paid', { id: 1 });
// @ts-expect-error unknown event name
bus.on('order.shipped', () => {});
```

Or `expectTypeOf(x).toEqualTypeOf<...>()` with the test runner's type-assertion helper. `@ts-expect-error` fails the build if the line ever stops erroring, so regressions surface.

## Keep the checker fast

- Name complex types (aliases/interfaces get cached; inline anonymous types are recomputed).
- `interface A extends B, C` over `B & C & {...}` for object composition.
- Avoid recursive conditional types over big inputs and unions producing thousands of members (template literal × union explodes quickly).
- Annotate exported function return types so consumers don't re-infer giant types.
- If `--extendedDiagnostics` shows type count/instantiations spiking after a change, simplify that type.

## When to stop

If a type needs a paragraph to explain, or error messages become unreadable for callers, step back: a simpler signature plus a runtime check or a small overload is usually better for the team.

## Checklist

- [ ] Every generic relates ≥2 positions and is constrained
- [ ] Built-in utility types used before custom ones
- [ ] Public complex types have `@ts-expect-error`/`expectTypeOf` tests
- [ ] No noticeable tsc slowdown from new types
