# Protobuf contracts with Buf

How to design, lint, evolve and generate `.proto` APIs for Go services. The
complete, lint-clean template is in
`assets/proto-templates/proto/acme/orders/v1/orders.proto`. Copy it rather than
writing a file from scratch.

## Contents

1. [Contract rules](#contract-rules)
2. [Buf workflow](#buf-workflow)
3. [Evolving an API safely](#evolving-an-api-safely)
4. [Code generation choices](#code-generation-choices)
5. [Validation with protovalidate](#validation-with-protovalidate)
6. [Working with generated Go code](#working-with-generated-go-code)

## Contract rules

| Rule | Why |
|---|---|
| `syntax = "proto3"`, package `org.service.v1`, directory `org/service/v1/` | Buf `STANDARD` lint enforces both. The version suffix lets `v2` live next to `v1` |
| One `XRequest` / `XResponse` message per RPC, even if it's empty | You can add fields later without changing the method signature |
| Enum zero value `X_UNSPECIFIED = 0`, every value prefixed `X_` | An unset field is distinguishable from a real state. Enum values share the package namespace |
| `reserved` numbers *and* names for removed fields | Reusing a field number reinterprets old bytes as the new field. That silently corrupts data |
| `google.protobuf.Timestamp` for instants, `Duration` for spans | Unambiguous UTC encoding across languages |
| Money as `int64` minor units (or `google.type.Money`) | `double` loses cents |
| List RPCs take `page_size` + `page_token` and return `next_page_token` | Opaque tokens let the server change its pagination strategy without breaking clients |
| Updates take a `google.protobuf.FieldMask update_mask` | Old clients don't wipe fields they've never heard of |
| Creates carry a client-generated `request_id` | Retries and redeliveries become idempotent |
| Put validation rules in the schema (protovalidate) | One source of truth that every language enforces |
| Comment every service, RPC and message | The comments become generated docs and show up in IDE hovers |

Server-streaming RPCs (`returns (stream X)`) suit change feeds and large result
sets. Bidirectional streams are harder to load-balance, retry and observe. Use
them only when both sides really interleave messages.

## Buf workflow

`assets/proto-templates/` has `buf.yaml` (module, deps, lint, breaking) and
`buf.gen.yaml` (plugins, managed mode). Put both at the repository root, next to
`go.mod`, and set `go_package_prefix` to `<your module path>/gen`.

```sh
buf dep update                               # resolve deps (protovalidate) into buf.lock
buf format -w                                # canonical formatting
buf lint                                     # STANDARD style rules
buf breaking --against '.git#branch=main'    # compare with main before merging
buf generate                                 # write Go code into gen/
```

In CI, run `buf format -d --exit-code`, `buf lint` and `buf breaking` on every
pull request that touches `proto/`. Breaking checks only work when they run
*before* merge.

## Evolving an API safely

`breaking: use: [FILE]` (the default) rejects anything that breaks generated
code or the wire format.

| Change | Safe? | Notes |
|---|---|---|
| Add a field, message, enum value, RPC | Yes | Old readers ignore unknown fields |
| Remove a field | Only with `reserved` | Keep the number and the name reserved forever |
| Rename a field | Wire-safe, not source-safe | Breaks generated code and JSON names. FILE flags it |
| Change a field's type or number | No | Corrupts data in flight and at rest |
| Change `optional` / `repeated` | No | Wire meaning changes |
| Change an RPC's request/response type | No | Add a new RPC instead |
| Rename a package | No | That is a new API: create `v2` |

For a breaking redesign, create `proto/acme/orders/v2/`. Serve v1 and v2 from the
same binary, with both delegating to one domain layer. Then remove v1 once
traffic shows no callers are left.

## Code generation choices

**Remote plugins** (the template's default) run on the Buf Schema Registry. You
don't install anything locally. Pin the plugin versions to match go.mod:
`buf.build/protocolbuffers/go:v1.36.12` and `buf.build/grpc/go:v1.6.2`.

**Local plugins through the go.mod `tool` directive** (Go 1.24+) work offline.
They pin the plugin version in go.mod, next to the runtime library it has to
match:

```sh
go get -tool google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
go get -tool google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
```

```yaml
version: v2
clean: true
managed:
  enabled: true
  disable:
    - file_option: go_package
      module: buf.build/bufbuild/protovalidate
  override:
    - file_option: go_package_prefix
      value: example.com/skeleton/gen
plugins:
  - local: ["go", "tool", "protoc-gen-go"]
    out: gen
    opt: paths=source_relative
  - local: ["go", "tool", "protoc-gen-go-grpc"]
    out: gen
    opt: paths=source_relative
```

Keep the `disable` entry for protovalidate. Without it, managed mode rewrites
that module's `go_package` too, and the generated imports point at a package
that doesn't exist.

Choose once whether `gen/` is committed or generated in CI, and stick to it.
Committing it makes `go get` of your module work and makes code review show
API diffs. Generating in CI avoids merge conflicts in generated files.

## Validation with protovalidate

`protoc-gen-validate` (PGV) is in maintenance mode and its repository is
archived. Use **protovalidate** instead:
- In `.proto` files, the rules come from `import "buf/validate/validate.proto"`.
- In Go, use `buf.build/go/protovalidate` v1.4.0, which needs Go 1.26, the same
  as the skill's floor.

Rules are evaluated at runtime, so no extra code generation step is needed.

Enforce the rules in one interceptor, not by hand in each handler:

```go
// Package validation rejects invalid requests before handlers run.
package validation

import (
	"fmt"

	"buf.build/go/protovalidate"
	protovalidatemw "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/protovalidate"
	"google.golang.org/grpc"
)

// ServerOptions returns interceptors that answer codes.InvalidArgument for
// any request violating the rules in its .proto file. Pass them through
// grpcserver.Config.ServerOptions; they run innermost, after recovery and
// logging, so rejected calls are still logged and counted.
func ServerOptions() ([]grpc.ServerOption, error) {
	v, err := protovalidate.New()
	if err != nil {
		return nil, fmt.Errorf("protovalidate: %w", err)
	}
	return []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(protovalidatemw.UnaryServerInterceptor(v)),
		grpc.ChainStreamInterceptor(protovalidatemw.StreamServerInterceptor(v)),
	}, nil
}
```

Test the rules themselves, because they are part of the contract:

```go
package validation

import (
	"strings"
	"testing"

	"buf.build/go/protovalidate"

	ordersv1 "example.com/skeleton/gen/acme/orders/v1"
)

func TestCreateOrderRules(t *testing.T) {
	v, err := protovalidate.New()
	if err != nil {
		t.Fatal(err)
	}
	valid := &ordersv1.CreateOrderRequest{
		RequestId:    "6f1c2a7e-3b4d-4e8f-9a0b-1c2d3e4f5a6b",
		CustomerId:   "cust-1",
		Items:        []*ordersv1.LineItem{{Sku: "SKU-1", Quantity: 2, UnitPriceCents: 1999}},
		CurrencyCode: "USD",
	}
	if err := v.Validate(valid); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}

	invalid := &ordersv1.CreateOrderRequest{RequestId: "not-a-uuid", CustomerId: "cust-1", CurrencyCode: "USD"}
	err = v.Validate(invalid)
	if err == nil {
		t.Fatal("invalid request accepted")
	}
	for _, field := range []string{"request_id", "items"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("violation for %s missing in %q", field, err)
		}
	}
}
```

## Working with generated Go code

- **Read fields with getters** (`req.GetOrder().GetId()`). They are nil-safe at
  every level, while direct field access panics on a nil sub-message.
- **Never copy message values** (`m2 := *m1`). Generated structs contain
  internal state, and `go vet` reports `copylocks`. Use `proto.Clone(m)` and
  compare with `proto.Equal`.
- **Embed `UnimplementedXServer` by value** in server structs. Then adding an
  RPC to the proto doesn't break the build, and calling the new method returns
  `Unimplemented` instead of panicking.
- **Generated streaming types are generic aliases** (protoc-gen-go-grpc 1.5+).
  For example, `OrderService_WatchOrdersServer` is
  `grpc.ServerStreamingServer[WatchOrdersResponse]`, so helper code can be
  written once and work for every stream.
- **Opaque API / editions:** files declared `edition = "2024"` generate the
  *Opaque* Go API by default. Fields are hidden, so you use only
  `GetX`/`SetX`/`HasX`, and you build messages with builders. `proto3` files
  keep the open-struct API used throughout this skill. Don't switch a module's
  API level casually, because every call site changes.
- **Use one protobuf library.** Only use `google.golang.org/protobuf`. The
  legacy `github.com/golang/protobuf` package exists only as a shim.
