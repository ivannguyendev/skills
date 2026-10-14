# Code style and review

Team conventions that keep a large Go codebase consistent and safe to change. Follow the project on taste; follow this file when the project relies on a removed API or hides a bug class.

## Contents

1. [Version feature map](#version-feature-map)
2. [Naming](#naming)
3. [Packages](#packages)
4. [Interfaces and constructors](#interfaces-and-constructors)
5. [Errors](#errors)
6. [Context and concurrency in APIs](#context-and-concurrency-in-apis)
7. [Logging](#logging)
8. [Generics and docs](#generics-and-docs)
9. [Tests](#tests)
10. [Lint and CI](#lint-and-ci)
11. [Review checklist](#review-checklist)

## Version feature map

Semantics follow the `go` line in go.mod, not the installed toolchain.

| Go | Use freely from here |
|---|---|
| 1.22 | Per-iteration loop vars (drop `v := v`), `for i := range n`, `ServeMux` patterns, `math/rand/v2` |
| 1.23 | Iterators (`iter.Seq`, `slices.Collect`); unstopped timers are collectable |
| 1.24 | `tool` directive (`go get -tool`), `b.Loop()`, `t.Context()`, `os.Root`, JSON `omitzero` |
| 1.25 | `wg.Go`, `testing/synctest`, container-aware GOMAXPROCS, FlightRecorder |
| 1.26 | `errors.AsType`, `new(expr)`, Green Tea GC, `slog.NewMultiHandler`, `go fix` modernizers |
| 1.27 | Generic methods, `encoding/json/v2`, `goroutineleak` profile |

Also retire: `interface{}` → `any`, `ioutil.*` → `io`/`os`, `time.After` inside `for { select }` → one reused `Timer`, `log.Printf` → `slog`.

## Naming

- Packages: short, lowercase, one word that says what it provides (`orders`, `grpcserver`). Never `util`, `common`, `helpers`, `base`, `models`, `types`.
- No stutter: `orders.Service`, not `orders.OrderService`.
- Initialisms keep one case: `ID`, `URL`, `HTTPClient`, `userID`.
- No `Get` prefix on getters (`o.Status()`); generated protobuf is the exception.
- Errors: sentinels `ErrNotFound`, types `ValidationError` (`errname` linter).
- Receivers: one or two letters, the same on every method, never `this`/`self`.
- Files: snake_case (`error_mapping.go`). Never end a name in a GOOS/GOARCH word (`_linux.go`) unless you mean a build constraint.

## Packages

- Organize by domain, not by layer: `orders/` holds the model, rules and the interfaces it needs. A `models/`+`services/`+`handlers/` split spreads one feature over the tree.
- `internal/` by default; an exported package is an API you must keep stable.
- Small package APIs. No package-level mutable state, no I/O in `init`; constants, sentinel errors and compiled regexps are fine.

## Interfaces and constructors

- Interfaces live at the consumer with the 1–3 methods it calls; producers return concrete types. Add one only when a second implementation or a fake exists, never "for mocking".
- Constructors take required dependencies as parameters plus a `Config` struct; they default zero fields, validate, and return an error rather than panic. Functional options only for libraries with many knobs.
- Make the zero value useful or unreachable. Pass `*T` when `T` holds a mutex, atomics or protobuf state (`vet copylocks`).

## Errors

- Handle each error once: log it **or** return it.
- Wrap with what *this* function was doing: `fmt.Errorf("reserve %s: %w", sku, err)`. No "failed to".
- Sentinel to branch on, typed error to carry fields, `errors.Join` for several failures. `%w` only when callers may inspect the cause; `%v` hides an implementation detail.
- Inspect with `errors.Is` and `errors.AsType[*T](err)` (1.26).
- Panic only for programmer errors, never across a package boundary.

## Context and concurrency in APIs

- `ctx` is the first parameter of anything that blocks or does I/O; never stored in a struct.
- Only the creator cancels, with `defer cancel()` right after `WithTimeout`/`WithCancel`.
- Context values carry request-scoped data (trace, principal, tenant), never optional parameters.
- `WithCancelCause`/`Cause` say why work stopped; `AfterFunc` runs cleanup without parking a goroutine.
- Synchronous APIs: let callers choose concurrency. Keep channels out of public APIs unless streaming is the point.
- Document goroutine safety on every exported type.

## Logging

- One JSON `slog` logger per process, injected or set in `main`.
- `*Context` methods everywhere a ctx exists, so `trace_id` reaches the line (`sloglint` `context: scope`).
- Stable low-cardinality keys; never log secrets, tokens, bodies or personal data. Redact at the type with `slog.LogValuer`.
- Hot paths: `LogAttrs` with typed attrs; log errors and slow requests, not every request.

## Generics and docs

- Generics for containers, slice/map algorithms and identical plumbing (worker pools, `Result[T]`). Not for business logic: differing behaviour is an interface. Start concrete; generalize at the third copy.
- Every export has a doc comment starting with its name and stating its contract (concurrency safety, ownership, zero value). Comments say *why*. One package comment per package. `// Deprecated: use X.` instead of deleting.

## Tests

- Table-driven subtests whose names read as sentences; `t.Context()`, `b.Loop()` + `b.ReportAllocs()`.
- Fakes over mocks: a 20-line in-memory fake tests behaviour; generated mocks pin call order and break on refactors.
- Real stack where cheap: `httptest` with the real middleware chain, `bufconn` with the real gRPC server.
- `testing/synctest` for time and goroutines. Fixtures in `testdata/`, golden files with `-update`.
- `goleak.VerifyTestMain` in every package with goroutines; alloc budgets in `//go:build !race` files; fuzz every parser of external input.

## Lint and CI

The lint policy is code: `assets/service-skeleton/.golangci.yaml` (golangci-lint v2, zero issues on the skeleton). Beyond the standard set it enables linters that each catch a production bug class: `bodyclose`, `noctx`, `fatcontext`, `errorlint`, `nilerr`, `gosec`, `sloglint`, `spancheck`, `exhaustive`, `gocritic`, `revive`, `modernize`. `goimports.local-prefixes` is the module path.

- Keep it quiet: a noisy linter gets ignored. Every `//nolint` names the linter and a reason.
- Install the release binary, not `go install`.

```sh
go vet ./...
golangci-lint run ./...
go test -race -shuffle=on -count=1 ./...
go run golang.org/x/vuln/cmd/govulncheck@latest ./...   # newest release is not always patched
go fix ./...                                            # 1.26+ modernizers
```

## Review checklist

Check the hard rules in `SKILL.md` first, then:

- **API surface:** smallest export that works, interfaces at the consumer, doc comments on exports.
- **Errors:** wrapped with context, handled once, no internal text to clients.
- **Hot paths:** no `fmt.Sprintf`, reflection or per-call allocation a benchmark would flag; a changed alloc budget comes with a reason.
- **Tests:** behaviour tested through the real stack; `goleak` and `-race` green.
- **Lint and vulns:** `golangci-lint` and `govulncheck` clean; every new `//nolint` explained.

State each finding as the failure it causes ("leaks one goroutine per cancelled request"), not the rule name.
