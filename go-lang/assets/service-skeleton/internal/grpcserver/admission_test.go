package grpcserver_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"example.com/skeleton/internal/grpcserver"
	"example.com/skeleton/internal/resilience"
)

// With the shared limiter exhausted, application RPCs are shed with
// UNAVAILABLE while health checks keep answering, so an overloaded pod is
// not also marked unhealthy.
func TestLoadShedExemptsHealth(t *testing.T) {
	limiter := resilience.NewLimiter(1, 0)
	if err := limiter.Acquire(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer limiter.Release()

	h := start(t, grpcserver.Config{Limiter: limiter}, func(context.Context) error { return nil })

	if code := status.Code(callFake(t.Context(), h.conn)); code != codes.Unavailable {
		t.Errorf("application RPC under overload = %v, want Unavailable", code)
	}
	resp, err := healthpb.NewHealthClient(h.conn).Check(t.Context(), &healthpb.HealthCheckRequest{})
	if err != nil || resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("health check under overload = %v, %v; want SERVING", resp, err)
	}
}
