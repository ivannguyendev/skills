package grpcserver

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// drainingHealth ends Watch streams when the server starts draining.
//
// health.Server.Watch only returns when the stream's context ends. Clients
// that use Watch (Envoy, gRPC client-side health checking) would otherwise
// hold GracefulStop open until ShutdownTimeout and force a hard Stop on every
// deploy. Watchers still receive NOT_SERVING first, at health.Shutdown.
type drainingHealth struct {
	*health.Server
	draining <-chan struct{}
}

// Watch delegates to health.Server with a context that also ends on drain.
func (h *drainingHealth) Watch(req *healthpb.HealthCheckRequest, stream healthpb.Health_WatchServer) error {
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	go func() {
		select {
		case <-h.draining:
			cancel()
		case <-ctx.Done():
		}
	}()
	return h.Server.Watch(req, &ctxStream[healthpb.HealthCheckResponse]{
		ServerStreamingServer: stream,
		ctx:                   ctx,
	})
}

// ctxStream overrides the context of a server stream.
type ctxStream[T any] struct {
	grpc.ServerStreamingServer[T]
	ctx context.Context
}

func (s *ctxStream[T]) Context() context.Context { return s.ctx }
