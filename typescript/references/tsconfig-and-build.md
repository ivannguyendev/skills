# tsconfig & build playbook

Most "TypeScript is broken" incidents are config mismatches between how tsc resolves modules and how the runtime/bundler actually loads them.

## ❌ → ✅ Quick fixes

| ❌ Avoid | ✅ Prefer | Why |
|---|---|---|
| `paths: { "@/*": ["src/*"] }` then `node dist/` | `#src/*` subpath imports, or a bundler | `paths` isn't rewritten in output |
| toggling `esModuleInterop` to fix an ESM error | align `module`/`moduleResolution` with the runtime | treats the symptom |
| tsx / type stripping in prod with no type check | `tsc --noEmit` in CI | stripping doesn't type-check |
| `enum`/`namespace` with Node type stripping | `as const`; `erasableSyntaxOnly: true` | not erasable → fails at load |
| `include: ["**/*"]` | `include: ["src"]` | checks `dist/` and generated files |
| `types` listed last in an `exports` condition | `types` condition first | resolvers take the first match |

## Baselines

Node service (tsc emit or type stripping):

```jsonc
{
  "compilerOptions": {
    "target": "ES2023",
    "module": "NodeNext",              // matches Node's real ESM/CJS resolution
    "moduleResolution": "NodeNext",
    "strict": true,
    "noUncheckedIndexedAccess": true,
    "exactOptionalPropertyTypes": true, // when feasible
    "noImplicitOverride": true,
    "noFallthroughCasesInSwitch": true,
    "verbatimModuleSyntax": true,       // explicit `import type`, no surprise elision
    "isolatedModules": true,
    "skipLibCheck": true,
    "outDir": "dist",
    "sourceMap": true
  }
}
```

Bundled app (Vite, Next, esbuild…): `"module": "ESNext"`, `"moduleResolution": "Bundler"`, `"noEmit": true`; the bundler transpiles and tsc only type-checks.

## ESM / CJS

- `"type": "module"` in package.json → `.ts` files are ESM; with `NodeNext`, relative imports need the runtime extension (`./db.js`), or `.ts` with `rewriteRelativeImportExtensions` (TS ≥5.7).
- Mixed-format errors (`ERR_REQUIRE_ESM`, `default is not a function`) usually mean `module`/`moduleResolution` doesn't match the runtime. Fix the config, don't add `esModuleInterop` hacks blindly.
- `import type` for type-only imports so runtime never loads them.

## Path aliases

- `paths` is compile-time only. Node won't resolve `@/db` in `dist/` unless something rewrites it.
- Prefer package.json subpath imports (works in TS with NodeNext/Bundler and in Node at runtime, no extra tooling):

```jsonc
// package.json
{ "imports": { "#src/*": "./dist/*" } }
```

For TS to see sources during dev, use conditions (`"#src/*": { "types": "./src/*", "default": "./dist/*" }`) or let the bundler handle aliases. Otherwise use a bundler or a post-build rewrite step.

## Running TS directly

- Node type stripping (no flag on 22.18+/23.6+) handles erasable syntax only: no `enum`, `namespace`, parameter properties, `import =`. Enforce with `"erasableSyntaxOnly": true` (TS ≥5.8).
- Stripping and esbuild/swc don't type-check. CI must run `tsc --noEmit` (or `tsc -b`).

## Monorepo

- Project references: each package `"composite": true`, root `tsconfig` with `references`, build with `tsc -b` (incremental, only changed projects).
- Or internal packages consumed from source via `exports` + bundler; pick one model per repo.
- `isolatedDeclarations` (TS ≥5.5) lets `.d.ts` emit run in parallel/outside tsc, at the cost of explicit return types on exports.

## Slow tsc / IDE

1. Measure: `tsc --noEmit --extendedDiagnostics` (check time, types count, memory), then `tsc --generateTrace trace/` and inspect hot spots (`@typescript/analyze-trace`, or load the trace in a browser's performance panel).
2. Common causes: `include` sweeping `dist/`/generated files; huge unions or deep recursive conditional types; big intersections (prefer `interface extends`); inferring giant return types from ORMs/schemas (annotate exported types).
3. Fixes: `incremental: true`, `skipLibCheck: true`, project references, transpile with a fast tool and type-check separately.

## Publishing a typed package

```jsonc
{
  "type": "module",
  "exports": {
    ".": { "types": "./dist/index.d.ts", "import": "./dist/index.js", "require": "./dist/index.cjs" }
  },
  "files": ["dist"]
}
```

- `types` condition first; ship `declaration` + `declarationMap`; never rely on `paths` in published output.
- Validate resolution across `node16`/`bundler` consumers with a checker tool (e.g. arethetypeswrong) before release.
- Avoid the dual-package hazard: stateful modules (singletons, `instanceof` classes) loaded once as ESM and once as CJS become two copies.

## CI gate

`tsc --noEmit` (or `tsc -b`) + type-aware lint (`no-floating-promises`, `no-misused-promises`, `no-unsafe-*`, `switch-exhaustiveness-check`) + tests. Treat type errors as build failures, never `// @ts-ignore` them away; use `// @ts-expect-error <reason>` when unavoidable so it fails once fixed.

## Checklist

- [ ] `module`/`moduleResolution` match the runtime or bundler
- [ ] Aliases resolvable at runtime (subpath imports or bundler)
- [ ] Type check runs in CI separately from transpile
- [ ] Slow builds diagnosed with `--extendedDiagnostics`/trace before tweaking
- [ ] Packages: `exports` + `types` validated
