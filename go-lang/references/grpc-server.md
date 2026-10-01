# gRPC servers in Go

How to build the server side: options, interceptors, service implementations,
streaming, errors, auth, health and shutdown. The runnable baseline is
`assets/service-skeleton/internal/grpcserver/`. Every Go block below compiles
against code generated from the template proto (`ordersv1`) with grpc-go v1.83.2. Not v1.84.0: govulncheck flags it for GO-2026-6443, while v1.83.2 is the patched line.

## Contents

1. [Server options and why](#server-options-and-why)
2. [Interceptor order](#interceptor-order)
3. [Implementing a generated service](#implementing-a-generated-service)
4. [Server streaming that ends cleanly](#server-streaming-that-ends-cleanly)
5. [Bidirectional streams](#bidirectional-streams)
6. [Auth: metadata is untrusted input](#auth-metadata-is-untrusted-input)
7. [Error model](#error-model)
8. [Health, probes and reflection](#health-probes-and-reflection)
9. [Shutdown sequence](#shutdown-sequence)
10. [Transport security](#transport-security)

## Server options and why

| Option | Value in skeleton | Why |
|---|---|---|
| `grpc.StatsHandler(otelgrpc.NewServerHandler(...))` | health checks filtered out | Traces and RPC metrics. The otelgrpc *interceptors* were removed; stats handlers are the only API |
| `ChainUnaryInterceptor` / `ChainStreamInterceptor` | recovery → load shed (when `Config.Limiter` is set) → logging → error boundary | See [Interceptor order](#interceptor-order) |
| `KeepaliveParams.MaxConnectionAge` | 30m (+30s grace) | HTTP/2 connections are long-lived, so L4 load balancers never rebalance them. Ageing them out forces clients to reconnect and spread load |
| `KeepaliveParams.MaxConnectionIdle` | 5m | Frees resources held by idle clients |
| `KeepaliveParams.Time/Timeout` | 1m / 20s | Detects dead peers behind NATs that silently drop connections |
| `KeepaliveEnforcementPolicy.MinTime` | 15s | Clients that ping more often get GOAWAY `too_many_pings`. Keep it ≤ the client's keepalive `Time` |
| `KeepaliveEnforcementPolicy.PermitWithoutStream` | true | Must be true if clients send keepalives while they have no active RPCs |
| `MaxRecvMsgSize` | 4 MiB (the default, stated explicitly) | Limits memory per message. Raise it per service, not globally, and prefer streaming for large payloads |
| `grpc.Creds(...)` | mTLS when cert env vars are set | See [Transport security](#transport-security) |

## Interceptor order

The first interceptor in a chain is the outermost.
`ChainUnaryInterceptor` can be passed more than once, and each call appends to
the chain.

1. **Recovery** goes first, so a panic anywhere below it becomes
   `codes.Internal` and doesn't kill the process. Recovery logs the panic with
   its stack. The logging interceptor is unwound past, so it writes no line,
   but the stats handler still counts the RPC as `Internal`.
2. **Load shedding** (`LoadShedUnary`, `internal/grpcserver/admission.go`) uses
   the limiter shared with HTTP, and answers `UNAVAILABLE` when no slot frees up
   within the queue wait.
   - Health checks are exempt: shedding probes would mark an overloaded pod
     unhealthy and cascade the overload.
   - Shed RPCs skip the access log but are still counted by the stats handler.
3. **Logging and metrics** come next. They see the final status the client gets.
4. **Error boundary** (`ToStatus`) maps domain errors from `apperr`. It logs the
   raw error whenever the status the client gets hides a server-side fault.
5. **Auth** runs before any business logic. It rejects calls with
   `Unauthenticated` or `PermissionDenied`.
6. **Validation** (protovalidate) goes innermost, so rejected requests are still
   logged and counted.

Tracing is not an interceptor here. The stats handler wraps the whole chain, so
spans include interceptor time.

## Implementing a generated service

Keep the transport type thin:
1. decode the request with getters,
2. call the domain,
3. map domain values to proto messages.

Errors flow back unchanged, wrapped with context. The error boundary decides
the status code.

```go
// Package ordersgrpc adapts the orders domain to the generated gRPC API.
package ordersgrpc

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	ordersv1 "example.com/skeleton/gen/acme/orders/v1"
)

// Order is the domain type; the domain package never sees protobuf.
type Order struct {
	ID, CustomerID string
	TotalCents     int64
	CreatedAt      time.Time
}

// Repository returns apperr kinds (e.g. apperr.ErrNotFound) wrapped with %w.
type Repository interface {
	Get(ctx context.Context, id string) (Order, error)
	List(ctx context.Context, customerID string, limit int, pageToken string) ([]Order, string, error)
}

// Server implements ordersv1.OrderServiceServer.
type Server struct {
	// Embedded by value: RPCs added to the proto later return Unimplemented
	// instead of breaking the build or panicking on a nil pointer.
	ordersv1.UnimplementedOrderServiceServer
	repo Repository
}

// NewServer wires the transport to the domain.
func NewServer(repo Repository) *Server { return &Server{repo: repo} }

// Register attaches the service to any registrar, e.g. a *grpcserver.Server.
func (s *Server) Register(r grpc.ServiceRegistrar) { ordersv1.RegisterOrderServiceServer(r, s) }

// GetOrder returns one order.
func (s *Server) GetOrder(ctx context.Context, req *ordersv1.GetOrderRequest) (*ordersv1.GetOrderResponse, error) {
	o, err := s.repo.Get(ctx, req.GetId())
	if err != nil {
		return nil, fmt.Errorf("get order %s: %w", req.GetId(), err)
	}
	return &ordersv1.GetOrderResponse{Order: toProto(o)}, nil
}

const (
	defaultPageSize = 50
	maxPageSize     = 100
)

// ListOrders returns one page; page_size 0 means "server default".
func (s *Server) ListOrders(ctx context.Context, req *ordersv1.ListOrdersRequest) (*ordersv1.ListOrdersResponse, error) {
	size := int(req.GetPageSize())
	if size <= 0 { // negative only gets here without the validation interceptor
		size = defaultPageSize
	}
	orders, next, err := s.repo.List(ctx, req.GetCustomerId(), min(size, maxPageSize), req.GetPageToken())
	if err != nil {
		return nil, fmt.Errorf("list orders for %s: %w", req.GetCustomerId(), err)
	}
	resp := &ordersv1.ListOrdersResponse{NextPageToken: next, Orders: make([]*ordersv1.Order, 0, len(orders))}
	for _, o := range orders {
		resp.Orders = append(resp.Orders, toProto(o))
	}
	return resp, nil
}

func toProto(o Order) *ordersv1.Order {
	return &ordersv1.Order{
		Id:         o.ID,
		CustomerId: o.CustomerID,
		TotalCents: o.TotalCents,
		CreateTime: timestamppb.New(o.CreatedAt),
	}
}
```

## Server streaming that ends cleanly

A streaming handler has to return when any of three things happens:
- the client goes away (the stream context ends);
- the event source closes;
- the process starts shutting down.

The third one is easy to miss. `GracefulStop` waits for every handler but never
cancels their contexts, so a watch stream left alone blocks shutdown until the
hard-stop timeout. Hand the service `grpcserver.Server.Draining()` and select
on it.

Don't use the SIGTERM context for this. That context fires *before* the drain
delay, while the pod is still receiving traffic. Streams would end, clients would
reconnect to the same pod, and they'd be cut off again. `Draining()` closes after
the drain delay, right before `GracefulStop`, when load balancers have already
moved away. The skeleton handles the built-in health `Watch` stream the same
way.

```go
// Package watch implements a server-streaming RPC that honours shutdown.
package watch

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	ordersv1 "example.com/skeleton/gen/acme/orders/v1"
)

// Broker delivers change events; unsubscribe releases the subscription.
type Broker interface {
	Subscribe(ctx context.Context, customerID string) (events <-chan *ordersv1.WatchOrdersResponse, unsubscribe func())
}

// Server implements only WatchOrders; embed it alongside other handlers.
type Server struct {
	ordersv1.UnimplementedOrderServiceServer
	draining <-chan struct{} // grpcserver.Server.Draining()
	broker   Broker
}

// NewServer takes the server's drain signal, not a request context.
func NewServer(draining <-chan struct{}, b Broker) *Server {
	return &Server{draining: draining, broker: b}
}

// WatchOrders streams events until the client, the source or the process stops.
func (s *Server) WatchOrders(req *ordersv1.WatchOrdersRequest, stream ordersv1.OrderService_WatchOrdersServer) error {
	ctx := stream.Context()
	events, unsubscribe := s.broker.Subscribe(ctx, req.GetCustomerId())
	defer unsubscribe()
	return Pump(ctx, s.draining, stream, events)
}

// Pump forwards events to any server stream. It is generic over the
// response type, so every streaming RPC can reuse it.
func Pump[T any](ctx context.Context, draining <-chan struct{}, stream grpc.ServerStreamingServer[T], events <-chan *T) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err() // boundary maps to Canceled / DeadlineExceeded
		case <-draining:
			return status.Error(codes.Unavailable, "server shutting down, reconnect")
		case ev, ok := <-events:
			if !ok {
				return status.Error(codes.Unavailable, "event source closed")
			}
			if err := stream.Send(ev); err != nil {
				return err // client is gone; the error is already a status
			}
		}
	}
}
```

## Bidirectional streams

`Recv` blocks and can't be interrupted by a context. It returns only when the
client half-closes or when gRPC tears the stream down, which happens after the
handler returns. A handler that runs `Recv` in an errgroup and then waits for
that group deadlocks as soon as the sending side fails. So never *wait* for the
receiving goroutine. Return from the handler, and gRPC unblocks `Recv`. This was
checked against grpc-go v1.83/v1.84: 50 streams whose handler failed while the client
kept sending finished with the right status and leaked no goroutines.

```go
// Package bidi provides a safe request/response loop for bidi streams.
package bidi

import (
	"context"
	"errors"
	"io"

	"google.golang.org/grpc"
)

// Serve reads requests on one goroutine and answers them on the handler
// goroutine. It is safe to call Recv and Send concurrently on one stream,
// but never Send from two goroutines at once.
func Serve[Req, Res any](stream grpc.BidiStreamingServer[Req, Res], handle func(context.Context, *Req) (*Res, error)) error {
	ctx := stream.Context()
	reqs := make(chan *Req)
	recvErr := make(chan error, 1)

	// Not waited on: it exits on client half-close, on a receive error, or
	// once this handler returns and gRPC cancels the stream.
	go func() {
		defer close(reqs)
		for {
			req, err := stream.Recv()
			if err != nil {
				if !errors.Is(err, io.EOF) {
					recvErr <- err
				}
				return
			}
			select {
			case reqs <- req:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case req, ok := <-reqs:
			if !ok {
				select {
				case err := <-recvErr:
					return err
				default:
					return nil // client finished sending; we answered everything
				}
			}
			res, err := handle(ctx, req)
			if err != nil {
				return err
			}
			if err := stream.Send(res); err != nil {
				return err
			}
		}
	}
}
```

## Auth: metadata is untrusted input

Anything in incoming metadata comes from the caller. That includes tenant IDs,
user IDs and "internal" flags. Authenticate it, put the verified principal into
the context, and authorize from that principal, never from raw headers. For
streams, wrap the `ServerStream` so handlers see the enriched context.

```go
// Package auth authenticates bearer tokens for unary and streaming RPCs.
package auth

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Principal is the verified caller identity.
type Principal struct {
	Subject string
	Tenant  string
}

// ErrInvalidToken is what a Verifier returns for bad, expired or forged
// tokens. Any other error means verification itself failed (JWKS fetch,
// introspection outage) and maps to Unavailable, which clients may retry,
// instead of a misleading, non-retryable Unauthenticated.
var ErrInvalidToken = errors.New("invalid token")

// Verifier checks a token (JWT, opaque token introspection, ...).
type Verifier func(ctx context.Context, token string) (Principal, error)

type principalKey struct{}

// FromContext returns the principal set by the interceptors.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Unary authenticates every call except those in public (full method names).
func Unary(verify Verifier, public map[string]bool) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if public[info.FullMethod] {
			return handler(ctx, req)
		}
		ctx, err := authenticate(ctx, verify)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// Stream is the streaming counterpart of Unary.
func Stream(verify Verifier, public map[string]bool) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if public[info.FullMethod] {
			return handler(srv, ss)
		}
		ctx, err := authenticate(ss.Context(), verify)
		if err != nil {
			return err
		}
		return handler(srv, &wrappedStream{ServerStream: ss, ctx: ctx})
	}
}

// wrappedStream overrides Context so handlers see the principal.
type wrappedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedStream) Context() context.Context { return w.ctx }

func authenticate(ctx context.Context, verify Verifier) (context.Context, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	vals := md.Get("authorization") // keys are always lower-case in gRPC
	if len(vals) != 1 {
		return nil, status.Error(codes.Unauthenticated, "missing credentials")
	}
	scheme, token, ok := strings.Cut(vals[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" { // scheme is case-insensitive
		return nil, status.Error(codes.Unauthenticated, "malformed credentials")
	}
	p, err := verify(ctx, token)
	switch {
	case errors.Is(err, ErrInvalidToken):
		return nil, status.Error(codes.Unauthenticated, "invalid credentials") // don't echo why
	case err != nil:
		return nil, status.Error(codes.Unavailable, "cannot verify credentials now")
	}
	return context.WithValue(ctx, principalKey{}, p), nil
}
```

Make health checks public:
`map[string]bool{"/grpc.health.v1.Health/Check": true, "/grpc.health.v1.Health/Watch": true}`.
Probes and load balancers don't carry tokens. With mTLS, you can authorize the
*service* identity from `peer.FromContext(ctx)` → `credentials.TLSInfo` →
`State.VerifiedChains[0][0]`, for example the SPIFFE URI SAN.

## Error model

| Code | Use for | Retry? |
|---|---|---|
| `InvalidArgument` | The request is invalid regardless of system state | No |
| `NotFound` / `AlreadyExists` | Resource state | No |
| `FailedPrecondition` | The system isn't in the right state, e.g. the order is already shipped | No, until the state changes |
| `Aborted` | Concurrency conflict (optimistic lock) | Yes, at a higher level |
| `PermissionDenied` / `Unauthenticated` | Authenticated but not allowed / no valid identity | No |
| `ResourceExhausted` | Quota or rate limit | Yes, with backoff |
| `Unavailable` | Transient: overloaded, shutting down | Yes, if idempotent |
| `DeadlineExceeded` | The caller's budget ran out | Not within the same deadline |
| `Canceled` | The caller gave up | No |
| `Internal` / `Unknown` | Bugs and invariant violations | Page someone |

Choose codes by what the *caller* should do. Retry policies and alerts key off
them. otelgrpc marks server spans as errors only for `Unknown`,
`DeadlineExceeded`, `Unimplemented`, `Internal`, `Unavailable` and `DataLoss`.
Client spans are marked as errors for every non-OK code.

Add machine-readable details instead of encoding data in message strings:

```go
// Package rpcerr builds and reads structured gRPC error details.
package rpcerr

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// InvalidField reports which field failed and why, so clients can highlight it.
func InvalidField(field, description string) error {
	st := status.New(codes.InvalidArgument, "invalid request")
	detailed, err := st.WithDetails(&errdetails.BadRequest{
		FieldViolations: []*errdetails.BadRequest_FieldViolation{{Field: field, Description: description}},
	})
	if err != nil {
		return st.Err() // WithDetails fails only if the detail cannot be marshalled
	}
	return detailed.Err()
}

// FieldViolations extracts BadRequest details on the client side.
func FieldViolations(err error) []*errdetails.BadRequest_FieldViolation {
	for _, d := range status.Convert(err).Details() {
		if br, ok := d.(*errdetails.BadRequest); ok {
			return br.GetFieldViolations()
		}
	}
	return nil
}
```

## Health, probes and reflection

The skeleton registers `grpc.health.v1.Health` and marks every registered service,
plus the overall `""` entry, as `SERVING`. During shutdown it calls
`health.Shutdown()`, which sets everything to `NOT_SERVING`. Kubernetes 1.27+
can probe gRPC natively:

```yaml
readinessProbe:
  grpc:
    port: 50051 # checks the "" (overall) service
  periodSeconds: 5
livenessProbe:
  grpc:
    port: 50051
  periodSeconds: 10
  failureThreshold: 3
```

Liveness should answer "is the process wedged?", never "is the database up?".
Otherwise a dependency outage restarts every pod at once. Readiness can include
dependency checks: call `srv.Health().SetServingStatus("", NOT_SERVING)` while
the database is unreachable.

**mTLS and kubelet probes:** native gRPC probes are plaintext and can't present
a client certificate. With the skeleton's `RequireAndVerifyClientCert`, they
fail the handshake and the pod never becomes Ready. Options:
- probe the skeleton's private admin port instead (`/livez`, `/readyz` in
  `internal/admin`). It is plaintext, never public, and reflects the same drain
  state, because shutdown flips readiness and gRPC health together. This is the
  simplest option;
- serve the same `health.Server` on a second, plaintext, health-only listener
  (another `grpc.Server` on its own port);
- use an `exec` probe running `grpc_health_probe` with its TLS client-cert
  flags;
- let a mesh terminate mTLS, in which case the app listens in plaintext behind
  it.

Enable **reflection** only in development (`GRPC_REFLECTION=true`), so that
`grpcurl -plaintext localhost:50051 list` works. In production, reflection
gives your whole schema to anyone who can connect.

## Shutdown sequence

`grpcserver.Server.Serve` runs this order when the root context is cancelled:

1. `health.Shutdown()`, so probes and health-checking load balancers stop routing
   to this pod. `Watch` clients receive `NOT_SERVING`.
2. Wait `DrainDelay` so endpoint removal propagates. Kubernetes removes
   endpoints asynchronously. 3–5s is typical.
3. Close `Draining()`. Streaming handlers, and the health `Watch` streams, return.
4. `GracefulStop()`. New RPCs are refused and in-flight ones finish.
5. If that exceeds `ShutdownTimeout`, call `Stop()`, which cancels everything
   still running.
6. Back in `main`, flush telemetry last, using a fresh context with a timeout.

A signal that arrives during boot, before `Serve` has started, still counts as a
clean exit.

Set the pod's `terminationGracePeriodSeconds` higher than DrainDelay +
ShutdownTimeout + telemetry flush. Otherwise the kubelet sends SIGKILL in the
middle of the drain.

## Transport security

- **Defaults:** encrypt everything with TLS 1.3, and use mTLS between services.
  `internal/tlsconfig` builds both sides. It reloads the leaf key pair when
  either file changes and logs any reload that fails, so cert-manager or SPIFFE
  rotation works without a restart. A changed CA bundle still needs a restart.
- **Configuration:** set all three `TLS_*` variables or none. A partial set is a
  startup error, never a silent fallback to plaintext.
- **Client verification:** use `tls.RequireAndVerifyClientCert` with a dedicated
  internal CA. Don't use the system roots for internal identities.
- **Metadata limits:** cap header and metadata size with
  `grpc.MaxHeaderListSize`. Never log raw `authorization` metadata.
- **Service mesh:** if a mesh (Istio, Linkerd) terminates mTLS for you, don't
  layer a second TLS session inside it. Authorize from the identity the mesh
  provides.
