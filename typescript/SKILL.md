---
name: typescript
description: Production-grade TypeScript - strict, honest types that catch bugs at compile time without slowing the team down. Use whenever writing, refactoring or reviewing TypeScript, or when the user mentions tsconfig, strict mode, `any`/`as` casts, type errors, generics, mapped/conditional types, type-safe APIs, schema validation (zod etc.), discriminated unions, JS→TS migration, slow tsc/IDE, path alias or ESM/CJS build problems, monorepo builds or publishing typed packages, even if they don't name this skill.
---

# TypeScript

Goal: types that tell the truth. A wrong type is worse than no type because everyone trusts it. Reply in the user's language.

## Workflow

1. **Detect context first**: TS version, `tsconfig` (strictness, `module`/`moduleResolution`, `paths`), how code runs (tsc emit, bundler, Node type stripping, tsx), schema/validation lib, lint setup, app vs published library. Reuse what the repo has.
2. **Pick playbooks** from the table and read only those that match. Most tasks need 1-2.
3. **Write the code**, then **self-check** against the core rules and the runtime essentials. Fix violations rather than listing caveats.
4. If the repo's config blocks a rule (non-strict legacy code), follow the repo, say so in one line, and suggest the smallest step toward the rule.

## Core type rules

- `strict` on. New code compiles under `noUncheckedIndexedAccess`.
- No `any`. Use `unknown` + narrowing; `catch (err)` is `unknown`.
- **Types are erased at runtime.** External data (request, `JSON.parse`, env, queue, third-party API, storage) is `unknown` until a runtime schema parses it; derive the type from the schema.
- No `as` on external data, no `!` non-null on values that can really be missing. `as` only where the compiler can't follow a proven invariant, with a comment.
- `satisfies` to check literals without widening.
- Model states as discriminated unions with an exhaustive `never` check, not optional-field soup or boolean flags.
- Brand IDs and units (`UserId`, `MinorUnits`) when mixing them would be a real bug.
- Expected failures as result unions; bugs as thrown `Error`s.
- `readonly` for inputs you must not mutate; `as const` objects over `enum`.
- Explicit return types on exported functions; let inference handle locals.
- Prefer simple types over clever ones. If a type needs a paragraph to explain, simplify it or isolate it behind a named alias with type tests.

## Runtime essentials (types don't prevent these)

- Every network/DB call has a timeout and an `AbortSignal`; no floating promises, no `forEach(async)`; bounded concurrency.
- Every listener/timer/subscription has a dispose; every cache has a cap.
- No sync/CPU-heavy work on request paths; no O(n²) on user-scaled data.
- Money as integer minor units or a decimal type; IDs > 2^53 as strings.
- Errors wrapped with `{ cause }`, never swallowed; clients get generic messages, logs get stacks without secrets.
- Parameterized queries; no `eval`/`innerHTML` with dynamic data; merges guarded against `__proto__`.

## Playbooks

| File                                      | Read when                                                                                              |
| ----------------------------------------- | ------------------------------------------------------------------------------------------------------ |
| `references/tsconfig-and-build.md`        | tsconfig, strict flags, ESM/CJS, path aliases, type stripping, monorepo, slow tsc, publishing packages |
| `references/boundaries-and-validation.md` | request bodies, env, JSON, API clients, `as` casts, error handling                                     |
| `references/domain-modeling.md`           | designing types for business entities, states, IDs, money, results                                     |
| `references/advanced-types.md`            | generics, mapped/conditional/template literal types, type-safe helpers, type tests                     |
| `references/js-to-ts-migration.md`        | migrating JS to TS, adopting strict mode, reducing `any` in a legacy codebase                          |

## Output

- **Code task**: the code, then ≤120 words on non-obvious type decisions.
- **Review**: findings ranked by bug risk, each ≤2 lines: `file:line` - problem - fix; minor style issues share one line. ≤350 words outside code; code only for the top 1-3 fixes.
- **Type error / build problem**: the cause in 1-2 lines, the fix, and how to verify (`tsc --noEmit`, the failing command).
- Offer deeper detail in one line instead of including it.
