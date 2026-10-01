// Package grpcclient creates long-lived, instrumented gRPC client connections.
package grpcclient

import (
	"errors"
	"fmt"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

// DefaultServiceConfig balances across every resolved address and retries
// only methods listed by name. List idempotent reads only: retrying a write
// that reached the server can apply it twice. DEADLINE_EXCEEDED is not
// retryable because retries share the caller's single deadline.
//
// round_robin needs multiple addresses from the resolver, e.g. a Kubernetes
// headless Service via "dns:///orders.prod.svc.cluster.local:50051".
//
// retryThrottling is gRPC's built-in retry budget: each failure costs a token,
// each success refunds tokenRatio, and retries stop at or below half the
// tokens, so an outage cannot turn every client into a load multiplier. It
// only throttles methods that have a retryPolicy: add your idempotent methods
// to methodConfig.
const DefaultServiceConfig = `{
  "loadBalancingConfig": [{"round_robin": {}}],
  "retryThrottling": {"maxTokens": 10, "tokenRatio": 0.1},
  "methodConfig": [{
    "name": [{"service": "grpc.health.v1.Health", "method": "Check"}],
    "timeout": "2s",
    "retryPolicy": {
      "maxAttempts": 3,
      "initialBackoff": "0.1s",
      "maxBackoff": "1s",
      "backoffMultiplier": 2,
      "retryableStatusCodes": ["UNAVAILABLE"]
    }
  }]
}`

// Options configures New.
type Options struct {
	// Creds is required. Use insecure.NewCredentials() explicitly for local
	// development so plaintext is always a visible decision.
	Creds credentials.TransportCredentials
	// ServiceConfig overrides DefaultServiceConfig when non-empty.
	ServiceConfig string
	// DialOptions are appended last. Single-valued options (e.g.
	// WithDefaultServiceConfig) replace the defaults; list-valued ones
	// (stats handlers, interceptors) accumulate, so adding another
	// otelgrpc handler here would produce duplicate spans.
	DialOptions []grpc.DialOption
}

// New returns a client connection for target. NewClient performs no I/O: the
// channel connects on the first RPC (or conn.Connect()) and reconnects on its
// own, so create one per target at startup and share it across goroutines.
// The default resolver is "dns"; use "passthrough:///addr" to skip resolution.
func New(target string, opts Options) (*grpc.ClientConn, error) {
	if opts.Creds == nil {
		return nil, errors.New("grpcclient: Options.Creds is required")
	}
	sc := opts.ServiceConfig
	if sc == "" {
		sc = DefaultServiceConfig
	}

	dialOpts := []grpc.DialOption{
		grpc.WithTransportCredentials(opts.Creds),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
		grpc.WithChainUnaryInterceptor(wrapDownstreamUnary),
		grpc.WithDefaultServiceConfig(sc),
		// Time must be >= the server's EnforcementPolicy.MinTime (15s in
		// grpcserver) or the server closes the connection for pinging too often.
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	}
	dialOpts = append(dialOpts, opts.DialOptions...)

	conn, err := grpc.NewClient(target, dialOpts...)
	if err != nil {
		return nil, fmt.Errorf("grpc client %q: %w", target, err)
	}
	return conn, nil
}
