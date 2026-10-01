# gRPC clients and testing

How to create, configure and call gRPC client connections, and how to test
servers and clients without real networks. The runnable baseline is
`assets/service-skeleton/internal/grpcclient/`.

## Contents

1. [NewClient, not Dial](#newclient-not-dial)
2. [Connection lifecycle](#connection-lifecycle)
3. [Service config: load balancing, retries, timeouts](#service-config-load-balancing-retries-timeouts)
4. [Calling a dependency from a handler](#calling-a-dependency-from-a-handler)
5. [Testing over bufconn](#testing-over-bufconn)
6. [Testing interceptors directly](#testing-interceptors-directly)

## NewClient, not Dial

`grpc.Dial` and `grpc.DialContext` are deprecated. They still work throughout
1.x, but new code uses `grpc.NewClient`. The two differ in ways that matter:

| | `grpc.NewClient` | `grpc.Dial` (deprecated) |
|---|---|---|
| I/O at creation | None. The channel starts idle and connects on the first RPC or `conn.Connect()` | Starts connecting immediately |
| Default resolver | `dns` | `passthrough` |
| `WithBlock`, `WithReturnConnectionError`, `FailOnNonTempDialError` | Not supported | Supported (and an anti-pattern) |
| Target for an in-memory or custom dialer | `"passthrough:///bufnet"` | `"bufnet"` |

Don't try to recreate `WithBlock` by waiting for `Ready` at startup. That makes
deploy order matter and turns a dependency blip into a crash loop. Let the first
RPC fail with `Unavailable`, and let retries and readiness handle it.

## Connection lifecycle

- **Create one `*grpc.ClientConn` per target at startup and share it.** It is
  safe for concurrent use. It multiplexes RPCs over HTTP/2 and reconnects with
  backoff on its own. Creating a connection per request pays TCP + TLS + HTTP/2
  setup on every call, and it exhausts sockets.
- Generated stubs (`ordersv1.NewOrderServiceClient(conn)`) are cheap. Build them
  wherever they're needed.
- Close connections during shutdown, *after* the server has stopped, because
  in-flight handlers may still be calling out.
- **Keepalive pairing:** the client's `keepalive.ClientParameters.Time` has to
  be ≥ the server's `EnforcementPolicy.MinTime`, or the server closes the
  connection with GOAWAY `too_many_pings`. The skeleton uses 30s on the client
  and 15s on the server. A client `Time` below 10s is raised to 10s.

## Service config: load balancing, retries, timeouts

`grpcclient.DefaultServiceConfig` shows the shape:

- **`round_robin`** spreads RPCs across every address the resolver returns. It
  needs more than one address, so in Kubernetes point
  `dns:///svc.ns.svc.cluster.local:50051` at a **headless** Service
  (`clusterIP: None`). With a normal ClusterIP Service, DNS returns one virtual
  IP, every RPC rides one long-lived connection, and kube-proxy cannot rebalance
  it.
- **`retryThrottling`** (`maxTokens`, `tokenRatio`) is gRPC's built-in retry
  budget. Failures spend tokens and successes refund them, so a dependency
  outage cannot multiply load. The skeleton sets it in `DefaultServiceConfig`.
- **`retryPolicy`** applies to the methods you list *by name*. List only
  idempotent methods: a retried write that already reached the server applies
  twice unless it carries an idempotency key. Retry on `UNAVAILABLE`. Retrying
  on `DEADLINE_EXCEEDED` is useless, because every attempt shares the caller's
  single deadline.
- **`timeout`** in methodConfig is a ceiling. A shorter deadline on the
  caller's context still wins.

`grpc.NewClient` parses the service config immediately, so a unit test that
calls `grpcclient.New` catches JSON mistakes. The skeleton's
`grpc_client_test.go` does this.

## Calling a dependency from a handler

Wrap the generated stub in a small client that does three things:
- **bounds each call.** `context.WithTimeout` never extends the caller's
  deadline; the earlier deadline wins. The remaining budget therefore flows
  downstream automatically, *as long as you pass the request's ctx*.
- **translates status codes into this service's own error kinds.** A
  dependency's `InvalidArgument` is your bug, not your caller's.
- **adds outgoing metadata** where it's needed. Incoming metadata is never
  forwarded on its own, and that's by design.

```go
// Package ordersdep is this service's view of the orders dependency.
package ordersdep

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	ordersv1 "example.com/skeleton/gen/acme/orders/v1"
	"example.com/skeleton/internal/apperr"
)

// Client is safe for concurrent use; build it once with the shared conn.
type Client struct {
	stub    ordersv1.OrderServiceClient
	timeout time.Duration
}

// New wraps conn (a *grpc.ClientConn, or a fake in tests).
func New(conn grpc.ClientConnInterface, timeout time.Duration) *Client {
	return &Client{stub: ordersv1.NewOrderServiceClient(conn), timeout: timeout}
}

// Get fetches one order within the caller's remaining budget.
func (c *Client) Get(parent context.Context, id string) (*ordersv1.Order, error) {
	ctx, cancel := context.WithTimeout(parent, c.timeout) // never extends the caller's deadline
	defer cancel()
	ctx = metadata.AppendToOutgoingContext(ctx, "x-caller", "billing")

	resp, err := c.stub.GetOrder(ctx, &ordersv1.GetOrderRequest{Id: id})
	switch status.Code(err) {
	case codes.OK:
		return resp.GetOrder(), nil
	case codes.NotFound:
		return nil, fmt.Errorf("order %s: %w", id, apperr.ErrNotFound)
	case codes.DeadlineExceeded, codes.Canceled:
		if parent.Err() != nil {
			return nil, parent.Err() // the caller's own budget ran out or it went away
		}
		return nil, fmt.Errorf("get order %s: %w: %w", id, apperr.ErrUnavailable, err) // our per-call cap fired
	case codes.Unavailable, codes.ResourceExhausted:
		return nil, fmt.Errorf("get order %s: %w: %w", id, apperr.ErrUnavailable, err)
	default:
		return nil, fmt.Errorf("get order %s: %w", id, err) // the boundary turns this into Internal
	}
}
```

`apperr.ErrUnavailable` reaches your caller as `Unavailable`, a code they may
retry. Two safety nets cover the cases a wrapper misses:
- **The client interceptor.** `grpcclient.New` installs one that wraps every
  unary downstream error. A handler that just does `return nil, err` therefore
  never forwards a status unchanged. `status.Code(err)` still sees the original
  code through the wrap.
- **The server boundary.** `ToStatus` passes through only statuses created
  directly by the handler. From a *wrapped* downstream status it keeps only
  `Canceled`, `DeadlineExceeded` and `Unavailable`, and turns every other code
  into `Internal`.

For streaming calls, wrap errors yourself with `fmt.Errorf("…: %w", err)`, but
leave `io.EOF` unwrapped, because callers compare it with `==`.

## Testing over bufconn

`bufconn` is an in-memory listener. It runs the real gRPC stack, including
interceptors, stats handlers, status codes and metadata, with no ports and no
flakiness. Two details:
- With `NewClient`, use the target `passthrough:///bufnet` together with
  `WithContextDialer`.
- Use `t.Context()` so RPCs are cancelled when the test ends.

```go
package ordersgrpc_test

import (
	"context"
	"log/slog"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	ordersv1 "example.com/skeleton/gen/acme/orders/v1"
	"example.com/skeleton/internal/apperr"
	"example.com/skeleton/internal/grpcserver"
)

// fakeOrders is the handler under test; a real one would wrap a repository.
type fakeOrders struct {
	ordersv1.UnimplementedOrderServiceServer
	byID map[string]*ordersv1.Order
}

func (f *fakeOrders) GetOrder(_ context.Context, req *ordersv1.GetOrderRequest) (*ordersv1.GetOrderResponse, error) {
	o, ok := f.byID[req.GetId()]
	if !ok {
		return nil, apperr.ErrNotFound
	}
	return &ordersv1.GetOrderResponse{Order: o}, nil
}

// newClient serves svc through the production server stack in memory.
func newClient(t *testing.T, svc ordersv1.OrderServiceServer) ordersv1.OrderServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpcserver.New(grpcserver.Config{}, slog.New(slog.DiscardHandler))
	ordersv1.RegisterOrderServiceServer(srv, svc)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.ServeListener(ctx, lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		conn.Close()
		stop()
		if err := <-done; err != nil {
			t.Errorf("server: %v", err)
		}
	})
	return ordersv1.NewOrderServiceClient(conn)
}

func TestGetOrder(t *testing.T) {
	client := newClient(t, &fakeOrders{byID: map[string]*ordersv1.Order{"o-1": {Id: "o-1", TotalCents: 4200}}})

	tests := []struct {
		id   string
		want codes.Code
	}{
		{"o-1", codes.OK},
		{"missing", codes.NotFound},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			resp, err := client.GetOrder(t.Context(), &ordersv1.GetOrderRequest{Id: tt.id})
			if got := status.Code(err); got != tt.want {
				t.Fatalf("code = %v, want %v (err: %v)", got, tt.want, err)
			}
			if tt.want == codes.OK && resp.GetOrder().GetTotalCents() != 4200 {
				t.Errorf("unexpected order %v", resp.GetOrder())
			}
		})
	}
}
```

For integration tests against a real port, listen on `127.0.0.1:0` and pass
`lis.Addr().String()` to `NewClient` with the `passthrough:///` prefix. Port 0
picks a free port, so tests can run in parallel.

## Testing interceptors directly

An interceptor is a plain function, so you can call it with a fake handler. You
don't need a server or a connection.

```go
package interceptors_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"example.com/skeleton/internal/grpcserver"
)

func TestErrorBoundaryHidesInternals(t *testing.T) {
	icpt := grpcserver.ErrorBoundaryUnary(slog.New(slog.DiscardHandler))
	info := &grpc.UnaryServerInfo{FullMethod: "/acme.orders.v1.OrderService/GetOrder"}
	handler := func(context.Context, any) (any, error) {
		return nil, errors.New("pq: password authentication failed for user orders")
	}

	_, err := icpt(t.Context(), nil, info, handler)
	st := status.Convert(err)
	if st.Code() != codes.Internal || st.Message() != "internal error" {
		t.Fatalf("got %v %q, want Internal with a generic message", st.Code(), st.Message())
	}
}
```

Fakes beat mocks for gRPC dependencies. Implement the generated client interface
(`ordersv1.OrderServiceClient`) with a small struct that holds canned responses,
or serve a fake server over bufconn as above. Generated mocks tend to lock tests
to call order and argument details that don't matter.
