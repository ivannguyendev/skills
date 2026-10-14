# gRPC in production

## Contracts (Buf)

`assets/proto-templates/` holds a ready Buf v2 config and an example API (`acme/orders/v1`).

- `buf.yaml` v2: `modules: [{path: proto}]`, dep `buf.build/bufbuild/protovalidate`, `lint: [STANDARD]`, `breaking: [FILE]`.
- `buf.gen.yaml`: managed mode with `go_package_prefix: <module>/gen`; pin the `protocolbuffers/go` and `grpc/go` plugins to the go.mod versions.
- CI on PRs that touch `proto/`: `buf format -d --exit-code && buf lint && buf breaking --against '.git#branch=main'`.
- Package `org.svc.v1` lives in `org/svc/v1/`. Each RPC gets its own `XRequest`/`XResponse`.
- Enums start at `X_UNSPECIFIED`. Use `Timestamp`/`Duration`, money as int64 minor units, `page_token`, `FieldMask` for updates, and a client `request_id` for creates.
- Changes are additive only. Mark removed fields `reserved` (number **and** name). Put a redesign in `v2` beside `v1`, sharing the domain.
- Use protovalidate (not `protoc-gen-validate`) and `google.golang.org/protobuf` (not `golang/protobuf`). Regenerate generated code; never sed it.

### Changing an API

| Change | Safe? |
|---|---|
| Add a field, message, enum value, RPC | Yes: old readers ignore unknown fields |
| Remove a field | Only with `reserved` number **and** name, forever |
| Rename a field | Wire-safe, not source-safe: breaks generated code and JSON names |
| Change a field's type, number, or `optional`/`repeated` | No: corrupts data in flight and at rest |
| Change an RPC's request/response type | No: add a new RPC |
| Rename the package | No: that's `v2` |

Workflow: make the change additive → reserve what you remove → `buf breaking --against '.git#branch=main'` must pass before merge → for a redesign, add `proto/<org>/<svc>/v2/`, serve v1 and v2 from one binary over the same domain, delete v1 once traffic shows no callers.

## Server

```go
srv := grpc.NewServer(
	grpc.Creds(serverCreds),
	grpc.StatsHandler(otelgrpc.NewServerHandler(
		otelgrpc.WithFilter(filters.Not(filters.HealthCheck())))),
	grpc.ChainUnaryInterceptor(recoverUnary, LoadShedUnary(limiter), logUnary, errorBoundary, authUnary, validateUnary),
	grpc.ChainStreamInterceptor(recoverStream, logStream, errorBoundaryStream, authStream),
	grpc.KeepaliveParams(keepalive.ServerParameters{
		MaxConnectionAge: 30 * time.Minute, MaxConnectionAgeGrace: 30 * time.Second, // rebalance L4 LBs
		MaxConnectionIdle: 5 * time.Minute, Time: time.Minute, Timeout: 20 * time.Second,
	}),
	grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 15 * time.Second, PermitWithoutStream: true}),
	grpc.MaxRecvMsgSize(4<<20), // raise per service; stream large payloads
	grpc.MaxHeaderListSize(16<<10),
)
```

Interceptor order, outermost first: recover (a panic becomes `Internal`) → shed → log → error boundary → auth (metadata is untrusted) → validate. Enable reflection in dev only.

## mTLS

Cert, key and CA must be set all together or not at all; partial config is a startup error. Plaintext only with an explicit dev flag.

```go
func serverTLS(certFile, keyFile, caFile string) (*tls.Config, error) {
	pool, err := loadCA(caFile) // private CA, not system roots
	if err != nil {
		return nil, err
	}
	kp, err := newReloader(certFile, keyFile) // reloads on mtime change (cert-manager/SPIFFE)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:     tls.VersionTLS13,
		ClientCAs:      pool,
		ClientAuth:     tls.RequireAndVerifyClientCert,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return kp.get() },
	}, nil
}
// Client: RootCAs: pool, GetClientCertificate, TLS13 → credentials.NewTLS(cfg).
```

- If a reload fails, keep the old cert and warn. A CA change needs a restart.
- If a mesh terminates mTLS, don't nest TLS inside it; say so and authorize by mesh identity.
- Kubelet `grpc:` probes can't send a client cert, so use `httpGet` on the admin port (`/livez`, `/readyz`).

## Client

```go
const serviceConfig = `{
  "loadBalancingConfig": [{"round_robin": {}}],
  "retryThrottling": {"maxTokens": 10, "tokenRatio": 0.1},
  "methodConfig": [{
    "name": [{"service": "acme.inventory.v1.InventoryService", "method": "GetStock"}],
    "timeout": "2s",
    "retryPolicy": {"maxAttempts": 3, "initialBackoff": "0.1s", "maxBackoff": "1s",
      "backoffMultiplier": 2, "retryableStatusCodes": ["UNAVAILABLE"]}
  }]
}`

conn, err := grpc.NewClient("dns:///inventory.prod.svc.cluster.local:50051", // headless Service
	grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)),
	grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	grpc.WithDefaultServiceConfig(serviceConfig),
	grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second, PermitWithoutStream: true}),
)
```

- One `ClientConn` per target, shared. It does no I/O until the first RPC; never wait for Ready at boot, because that couples deploy order and causes crash loops.
- `round_robin` needs a headless Service; a ClusterIP pins all traffic to one connection.
- `retryPolicy` lists idempotent methods only. `retryThrottling` is the retry budget.
- Client keepalive `Time` ≥ server `MinTime`, or the server sends GOAWAY `too_many_pings`.
- A timeout on a non-idempotent call (charge, authorize) means the outcome is **unknown**. Never resubmit blindly: use an idempotency key, or reconcile by reading.
- Give each call `context.WithTimeout` and run it through the dependency's breaker. Close the conn after the server stops.

## Errors

- Choose codes by what the caller should do next:
  - `InvalidArgument`/`NotFound`/`FailedPrecondition`: no retry.
  - `Unavailable`: retry if idempotent.
  - `ResourceExhausted`: back off.
  - `Internal`: page.
- One boundary interceptor maps domain kinds. Wrap downstream errors (`fmt.Errorf("call %s: %w", m, err)`) so the boundary sanitizes them instead of forwarding.
- Put details in `errdetails` (`BadRequest`…), not in message strings.

## Streaming and shutdown

`GracefulStop` doesn't cancel handler contexts, so a stream must also watch `draining`.

```go
for {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-draining: // closed after DrainDelay, before GracefulStop
		return status.Error(codes.Unavailable, "shutting down, reconnect")
	case ev, ok := <-events:
		if !ok {
			return status.Error(codes.Unavailable, "source closed")
		}
		if err := stream.Send(ev); err != nil {
			return err
		}
	}
}
```

Shutdown order: `health.Shutdown()` → DrainDelay → close `draining` → `GracefulStop` with a timeout → `Stop`. Streams skip the limiter; cap them by count.

## Health, admin and OpenTelemetry

- Register `grpc.health.v1.Health`. Liveness ignores downstreams; readiness may check them.
- The admin `http.Server` (e.g. `:9090`, its own mux, not exposed) serves pprof, `/metrics`, `/livez` and `/readyz`.
- OTel:
  - OTLP trace and metric exporters → Collector;
  - resource: `service.name`/`version`/`deployment.environment.name`;
  - global propagator `TraceContext`+`Baggage` (without it nothing propagates);
  - parent-based ratio sampler;
  - `tp.Shutdown` runs last in `main` with a fresh timeout.
- otelhttp can't see chi's route pattern. Add a middleware inside chi that calls `span.SetName(r.Method+" "+rctx.RoutePattern())` and sets `semconv.HTTPRoute`. IDs go only in attributes.
- Use `slog.*Context` with a handler that adds `trace_id`/`span_id`.

## Tests

`bufconn.Listen(1<<20)` + `grpc.NewClient("passthrough:///bufnet", grpc.WithContextDialer(…))` runs the real server chain in-process. Assert with `status.Code(err)` and add `goleak.VerifyTestMain`.
