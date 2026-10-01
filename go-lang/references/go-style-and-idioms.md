# Go style and idioms for enterprise teams

Conventions that keep a large Go codebase consistent, reviewable and safe to
change, plus the modern idioms that replace older habits. Every Go block is a
complete file that compiles with the skill's module floor, `go 1.26.0`.

## Contents

1. [Version feature map](#version-feature-map)
2. [Idioms to retire](#idioms-to-retire)
3. [Naming](#naming)
4. [Package design](#package-design)
5. [Interfaces, constructors and zero values](#interfaces-constructors-and-zero-values)
6. [Errors](#errors)
7. [Context](#context)
8. [Concurrency in APIs](#concurrency-in-apis)
9. [Logging](#logging)
10. [Generics](#generics)
11. [Iterators](#iterators)
12. [Comments and documentation](#comments-and-documentation)
13. [Testing conventions](#testing-conventions)
14. [Tooling and lint](#tooling-and-lint)
15. [Code review checklist](#code-review-checklist)

## Version feature map

Language semantics (loop variables, timers) follow the `go` line in `go.mod`,
not the installed toolchain. Read it first.

| Go | What changes in service code |
|---|---|
| 1.22 | Per-iteration loop variables. `for i := range n`. `ServeMux` method and wildcard patterns, `r.PathValue`. `math/rand/v2` |
| 1.23 | Iterators (`iter.Seq`, `slices.Collect`, `maps.Keys`). Unstopped timers are collectable; timer channels are unbuffered |
| 1.24 | `tool` directive in go.mod. `b.Loop()`, `t.Context()`. `os.Root`. `maphash.Comparable`. JSON `omitzero` |
| 1.25 | `wg.Go(f)`. `testing/synctest`. Container-aware GOMAXPROCS. `runtime/trace.FlightRecorder` |
| 1.26 | Green Tea GC by default. `errors.AsType[E]`. `new(expr)`. `slog.NewMultiHandler`. `go fix` modernizers |
| 1.27 ⚠ | Generic methods. `encoding/json/v2`. `goroutineleak` profile GA. `uuid` package |

⚠ means it needs a go line above this skill's 1.26 floor. Go 1.27's `go test`
runs the `stdversion` check, which flags such APIs under a lower go line.

## Idioms to retire

| If you see | Write instead | Why |
|---|---|---|
| `v := v` before a goroutine or closure | nothing | Each iteration has its own variable since 1.22 |
| `wg.Add(1); go func(){ defer wg.Done() … }()` | `wg.Go(func(){ … })` | You can't forget `Done` or misplace `Add` |
| `var t *T; errors.As(err, &t)` | `t, ok := errors.AsType[*T](err)` | One expression, type-checked |
| `p := new(T); *p = v` or a helper `ptr(v)` | `new(v)` | 1.26 allows an expression |
| `for i := 0; i < b.N; i++` | `for b.Loop()` | Setup excluded from timing; results not optimized away |
| `tools.go` with blank imports | `go get -tool pkg@v` + `go tool pkg` | Pinned in go.mod |
| `go.uber.org/automaxprocs` | delete it | The runtime reads cgroup CPU limits |
| `interface{}` | `any` | Same type, easier to read |
| `ioutil.*` | `io.*`, `os.*` | Deprecated since 1.16 |
| `time.After` in a `for { select }` | one reused `Timer`/`Ticker` | One allocation per iteration otherwise |
| `log.Printf` | `slog` with `*Context` methods | Structured, trace-correlated |

## Naming

- **Packages:**
  - A package name is short, lowercase and a single word (`grpcserver`,
    `orders`) that says what it provides.
  - Never use `util`, `common`, `helpers`, `base`, `models` or `types`. Those
    names attract unrelated code and import cycles.
- **No stutter:** callers write `orders.Service`, not `orders.OrderService`. The
  package name is part of every identifier.
- **Initialisms keep one case:** `ID`, `URL`, `HTTPClient`, `userID`, `ServeHTTP`.
- **Getters have no `Get`:** `o.Status()`, not `o.GetStatus()` (generated
  protobuf code is the exception). Setters are `SetStatus`.
- **Errors:** sentinels are `ErrNotFound`, types are `ValidationError`. The
  `errname` linter enforces this.
- **Receivers:** use one or two letters, the same in every method of a type
  (`s *Server`), never `this` or `self`.
- **Length follows scope:** `i`, `r`, `ctx` are fine in short bodies; exported
  identifiers and package-level variables say exactly what they are.
- **Files:** lowercase snake_case (`grpc_server.go`, `error_mapping.go`). Never
  end a file name in a GOOS or GOARCH word (`_linux.go`, `_arm64.go`) unless you
  mean it as a build constraint.

## Package design

- **Organize by domain, not by layer.** `orders/` holds the order model, its
  rules and the interfaces it needs. A `models/` + `services/` + `handlers/`
  split spreads one feature across the tree and forces exported internals.
- **Default to `internal/`.** An exported package is an API you have to keep
  stable for every importer.
- **Keep package APIs small.** Export what callers need, not what might be
  useful one day.
- **No package-level mutable state.** Globals and `init()` side effects (opening
  connections, reading env) make tests order-dependent and hide dependencies.
  Constants, sentinel errors and compiled regexps are fine.
- **Make dependencies point inwards.** Domain packages import neither the
  transport nor the database. Adapters import the domain. Only `main` (or one
  wiring package) imports everything. `references/architecture-and-http.md` has
  the full layout.

## Interfaces, constructors and zero values

- **Define interfaces where they are used**, with the one to three methods the
  consumer calls. The producer returns a concrete type. "Accept interfaces,
  return structs" keeps implementations free to grow and fakes trivial to write.
- **Don't predeclare interfaces "for mocking".** Add an interface at the
  consumer when a second implementation or a test fake actually exists.
- **Constructors:**
  - Take required dependencies as parameters, plus a `Config` struct for
    tunables.
  - Apply defaults for zero fields, validate, and return an error rather than
    panic.
  - Use functional options only in libraries with many optional knobs. For
    services, a `Config` struct maps directly onto loaded configuration and shows
    every setting in one place.
- **Make the zero value useful or unreachable.** Either `var b bytes.Buffer`
  just works, or the type has unexported fields and a `New`.
- **Don't copy values that contain a lock.** Pass `*T` when `T` holds
  `sync.Mutex`, `atomic.*` or generated protobuf state; `go vet` reports
  `copylocks`.

```go
// Package inventory shows the interface, constructor and config conventions.
package inventory

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Stock is what this package needs from storage. It lives here, next to the
// consumer, and lists only the methods actually called.
type Stock interface {
	Reserve(ctx context.Context, sku string, qty int) error
}

// Config holds tunables; zero values get defaults in New.
type Config struct {
	MaxQuantity int           // default 100
	Timeout     time.Duration // default 2s per reservation
}

// Service reserves stock. It is safe for concurrent use.
type Service struct {
	stock Stock
	cfg   Config
}

// New takes required dependencies as parameters and tunables as Config.
// It returns *Service, a concrete type, so the API can grow without
// breaking implementers.
func New(stock Stock, cfg Config) (*Service, error) {
	if stock == nil {
		return nil, errors.New("inventory: stock is required")
	}
	if cfg.MaxQuantity <= 0 {
		cfg.MaxQuantity = 100
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 2 * time.Second
	}
	return &Service{stock: stock, cfg: cfg}, nil
}

// Reserve validates input, then calls the store within its own time budget.
func (s *Service) Reserve(ctx context.Context, sku string, qty int) error {
	if qty <= 0 || qty > s.cfg.MaxQuantity {
		return fmt.Errorf("reserve %s: quantity %d outside 1..%d", sku, qty, s.cfg.MaxQuantity)
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	if err := s.stock.Reserve(ctx, sku, qty); err != nil {
		return fmt.Errorf("reserve %s: %w", sku, err)
	}
	return nil
}
```

## Errors

- **Return errors; handle each one exactly once.** Either log it or return it,
  never both, or every layer prints the same failure.
- **Add the context of what *this* function was doing.** Write
  `fmt.Errorf("reserve %s: %w", sku, err)`. Don't repeat "failed to", because
  the chain already reads as a story.
- **Pick the error form by what callers need:**
  - Sentinels (`ErrNotFound`) let callers branch on the kind of failure.
  - Typed errors carry fields.
  - `errors.Join` reports several independent failures together.
- **Use `%w` only when callers may inspect the cause.** `%v` deliberately hides
  an implementation detail that you don't want to become part of your API.
- **Panic only for programmer errors** (impossible states, misuse at init).
  Never let a panic cross a package boundary as a way to report a failure.
- **Keep transport out of domain errors.** Domain code returns neutral kinds,
  and one boundary maps them to status codes. The skeleton does this with
  `internal/apperr` and `internal/grpcserver/error_mapping.go`.

```go
// Package errs shows sentinel, typed and joined errors with Go 1.26 helpers.
package errs

import (
	"errors"
	"fmt"
)

// ErrNotFound is a sentinel: callers test it with errors.Is.
var ErrNotFound = errors.New("not found")

// ValidationError is a typed error: callers read its fields.
type ValidationError struct{ Field, Reason string }

func (e *ValidationError) Error() string { return e.Field + ": " + e.Reason }

// Find wraps with %w so the chain stays inspectable.
func Find(rows map[string]string, id string) (string, error) {
	if id == "" {
		return "", &ValidationError{Field: "id", Reason: "required"}
	}
	v, ok := rows[id]
	if !ok {
		return "", fmt.Errorf("find %q: %w", id, ErrNotFound)
	}
	return v, nil
}

// Classify is the inspecting side. errors.AsType (1.26) needs no target var.
func Classify(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, ErrNotFound):
		return "not found"
	}
	if ve, ok := errors.AsType[*ValidationError](err); ok {
		return "invalid " + ve.Field
	}
	return "unexpected"
}

// CloseAll reports every failure, not only the first.
func CloseAll(closers ...func() error) error {
	errs := make([]error, 0, len(closers))
	for _, c := range closers {
		errs = append(errs, c())
	}
	return errors.Join(errs...) // nil when all are nil
}
```

## Context

- `ctx` is the first parameter of anything that blocks or does I/O. Never store
  it in a struct, because the struct outlives the request.
- Only the function that creates a context cancels it, with `defer cancel()`
  directly after `WithTimeout` or `WithCancel`.
- Context values are for request-scoped data that crosses API boundaries: trace
  context, authenticated principal, tenant. They are never a way to pass
  optional parameters.
- Work that must outlive the request uses `context.WithoutCancel(ctx)` plus its
  own timeout and an owner that waits for it. See `references/concurrency-patterns.md`.
- `context.WithCancelCause` and `context.Cause` say *why* work stopped.
  `context.AfterFunc` runs cleanup without parking a goroutine.

## Concurrency in APIs

- **Don't start goroutines in constructors.** Expose `Run(ctx) error` (blocking
  until ctx ends) or `Start` with a matching `Close`/`Stop`. The caller then owns
  the lifecycle and can sequence shutdown.
- **Let callers choose concurrency.** A synchronous API can be called
  concurrently; an API that spawns goroutines internally can't be made
  synchronous again.
- **Document goroutine safety on every exported type**: "safe for concurrent
  use", or "not safe; one per goroutine".
- **Keep channels out of public APIs unless streaming is the point.** Callbacks
  or iterators are easier to make leak-free.
- **Bound everything:** every worker pool, queue, buffer and cache has a size
  limit chosen on purpose. See `references/performance-and-memory.md`.

## Logging

- **One logger per process:** a JSON `slog` logger, injected through
  constructors or set with `slog.SetDefault` in `main`.
- **Use the `*Context` variants** (`InfoContext`, `ErrorContext`) whenever a
  context exists. That is how `trace_id` reaches the log line, through the
  skeleton's `telemetry.TraceHandler`. The `sloglint` setting `context: scope`
  enforces it.
- **Use stable, low-cardinality keys** (`grpc.method`, `http.status`), and put
  variable data in values. Never log secrets, tokens, full request bodies or
  personal data.
- **Redact at the type, not the call site.** A `slog.LogValuer` gives every log
  line the same safe view.
- **On hot paths:**
  - Use `logger.LogAttrs` with typed attributes; it avoids boxing every
    argument into `any`.
  - Log errors and slow requests rather than every request. See `AccessLog` in
    `internal/httpserver/middleware.go`.

```go
// Package logging shows type-level redaction with slog.LogValuer.
package logging

import (
	"log/slog"
	"os"
)

// User's LogValue keeps the email out of every log line, no matter who logs it.
type User struct {
	ID    string
	Email string
}

// LogValue returns the loggable view of u.
func (u User) LogValue() slog.Value { return slog.GroupValue(slog.String("id", u.ID)) }

// New builds the process logger; LevelVar allows changing verbosity at runtime.
func New(level *slog.LevelVar) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}
```

## Generics

- **Good uses:**
  - type-safe containers (bounded caches, sets, a sharded map);
  - algorithms over slices and maps;
  - plumbing that is identical across types, such as `grpc.ServerStreamingServer[T]`
    helpers, worker pools and `Result[T]`.
- **Not for business logic.** If a behaviour differs per type, that's an
  interface. Generic "repositories" or "services" hide the real queries and grow
  type parameters until nobody can read them.
- **Start concrete.** Generalize when the third copy appears.
- **Use the narrowest constraint that works** (`comparable`,
  `cmp.Ordered`, or a small interface).

## Iterators

Range-over-func is a good fit for paginated APIs. The caller writes a plain
`for` loop, and `break` stops fetching more pages.

```go
// Package ordersclient wraps the generated client with convenience helpers.
package ordersclient

import (
	"context"
	"iter"

	ordersv1 "example.com/skeleton/gen/acme/orders/v1"
)

// All yields every order of a customer, requesting pages lazily.
func All(ctx context.Context, c ordersv1.OrderServiceClient, customerID string) iter.Seq2[*ordersv1.Order, error] {
	return func(yield func(*ordersv1.Order, error) bool) {
		req := &ordersv1.ListOrdersRequest{CustomerId: customerID, PageSize: 100}
		for {
			resp, err := c.ListOrders(ctx, req)
			if err != nil {
				yield(nil, err)
				return
			}
			for _, o := range resp.GetOrders() {
				if !yield(o, nil) {
					return
				}
			}
			if resp.GetNextPageToken() == "" {
				return
			}
			req.PageToken = resp.GetNextPageToken()
		}
	}
}
```

## Comments and documentation

- **Every exported identifier has a doc comment** that starts with its name and
  says what it does and any contract: concurrency safety, ownership, what zero
  means. `revive`'s `exported` rule enforces this.
- **Comments explain *why*:** the constraint, the incident, the trade-off. The
  code already says *what*. A comment that restates a line is noise and goes
  stale.
- **Every package has a package comment** (`// Package grpcserver builds …`) in
  exactly one file.
- **Deprecate, don't delete.** Use `// Deprecated: use X instead.` as its own
  paragraph; tools surface it to every caller.

## Testing conventions

- **Table-driven subtests** with `t.Run`. Name the cases so a failure reads as a
  sentence.
- **Use `t.Context()`** (1.24) instead of `context.Background()` in tests.
  **Use `b.Loop()`** in benchmarks.
- **Fakes over mocks.** A 20-line in-memory fake of a consumer-side interface
  tests behaviour. Generated mocks pin call order and arguments, and break on
  every refactor.
- **Test through the real stack where it's cheap:** `httptest` with the real
  middleware chain, and `bufconn` with the real gRPC server
  (`references/grpc-client-and-testing.md`).
- **Use `testing/synctest`** for anything involving time or goroutines. The
  clock is fake, so the tests are fast and deterministic.
- **Put fixtures in `testdata/`;** the go tool ignores it. For large expected
  outputs, use golden files plus an `-update` flag.
- **Run with `-race -shuffle=on`.** Put `goleak.VerifyTestMain` in every package
  that starts goroutines. Allocation budgets live in `//go:build !race` files.
- **Fuzz every parser** of external input (`func FuzzX(f *testing.F)`).

```go
package slug

import (
	"strings"
	"testing"
	"unicode"
)

// Make lower-cases s and joins its words with dashes.
func Make(s string) string {
	return strings.ToLower(strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}), "-"))
}

func TestMake(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"punctuation becomes a single dash", "Hello, World", "hello-world"},
		{"surrounding space is dropped", "  spaced   out ", "spaced-out"},
		{"empty stays empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Make(tt.in); got != tt.want {
				t.Errorf("Make(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func BenchmarkMake(b *testing.B) {
	for b.Loop() {
		Make("Hello, World and Everyone")
	}
}

func FuzzMake(f *testing.F) {
	f.Add("Hello, World")
	f.Fuzz(func(t *testing.T, s string) {
		if got := Make(s); strings.ContainsFunc(got, unicode.IsSpace) {
			t.Errorf("Make(%q) = %q contains whitespace", s, got)
		}
	})
}
```

## Tooling and lint

- **The lint policy is code.** `assets/service-skeleton/.golangci.yaml`
  (golangci-lint v2) enables the standard set plus linters that each catch a
  class of production bug:
  - `bodyclose` and `noctx`: leaked connections, uncancellable I/O;
  - `fatcontext`, `errorlint`, `nilerr`: context and error handling mistakes;
  - `gosec`: for example a server without `ReadHeaderTimeout`;
  - `sloglint`, `spancheck`: logging and tracing mistakes;
  - `exhaustive`, `gocritic`, `revive`, `modernize`.

  Install the release binary; the project advises against `go install`. The
  skeleton passes with zero issues.
- **Formatting:** `gofmt` plus `goimports` with `local-prefixes` set to your
  module, so imports group as stdlib, third-party, then this module.
- **Keep the lint policy quiet.** A noisy linter gets ignored, and then real
  findings are ignored with it. `contextcheck` is left out because it flags
  correct code that derives contexts from streams. Every `//nolint` names the
  linter and gives a reason.
- **Commands that run in CI:**

```sh
go vet ./...
golangci-lint run ./...
go test -race -shuffle=on -count=1 ./...
go run golang.org/x/vuln/cmd/govulncheck@latest ./...   # the newest release is not always patched
go fix ./...                                            # 1.26+: apply modernizers before reviewing diffs
```

`govulncheck` reports only vulnerabilities your code can actually reach. When
it flags a dependency, the fix is often the newest *patched* release, not the
newest release. When this skill was verified, grpc-go v1.84.0 was affected by
GO-2026-6443 and v1.83.2 was the patched line.

## Code review checklist

Report each finding as a concrete failure ("leaks one goroutine per cancelled
request"), not just the rule it breaks.

- **Lifecycle:** every goroutine has an owner and an exit path; every `cancel`
  is deferred; bodies, rows and streams are closed on every path.
- **Bounds:** queues, caches, pools, request bodies, page sizes and retry counts
  all have limits.
- **Timeouts:** every outbound call carries a deadline derived from the incoming
  context.
- **Errors:** wrapped with context and handled once; no internal text leaks to
  clients.
- **API surface:** the smallest exported surface that works; interfaces defined
  at the consumer; doc comments on exports.
- **Hot paths:** no `fmt.Sprintf`, reflection or per-call allocation that a
  benchmark would flag; any changed alloc budget comes with a justification.
- **Tests:** the behaviour is tested through the real stack where cheap; goleak
  and `-race` stay green.
- **Lint and vulnerabilities:** `golangci-lint` and `govulncheck` are clean, and
  every new `//nolint` carries a reason.
