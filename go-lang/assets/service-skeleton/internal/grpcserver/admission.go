package grpcserver

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"example.com/skeleton/internal/resilience"
)

// LoadShedUnary rejects unary RPCs with UNAVAILABLE when the process-wide
// limiter (shared with HTTP) has no slot within its queue wait. UNAVAILABLE
// is retryable, so clients and load balancers move to another replica.
//
// Health checks are exempt: shedding them under load would mark the pod
// unready and push its traffic onto replicas that are just as busy, which
// turns overload into a cascading outage. Streaming RPCs are long-lived and
// would hold a slot for minutes, so they are not limited here either; bound
// them by stream count or MaxConcurrentStreams instead.
func LoadShedUnary(l *resilience.Limiter) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if strings.HasPrefix(info.FullMethod, "/grpc.health.v1.Health/") {
			return handler(ctx, req)
		}
		if err := l.Acquire(ctx); err != nil {
			if errors.Is(err, resilience.ErrOverloaded) {
				return nil, status.Error(codes.Unavailable, "server overloaded, retry with backoff")
			}
			return nil, status.FromContextError(err).Err()
		}
		defer l.Release()
		return handler(ctx, req)
	}
}
