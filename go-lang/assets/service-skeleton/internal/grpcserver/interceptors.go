package grpcserver

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Interceptor order matters: the first in the chain is the outermost.
//
//	recovery → logging → error boundary → handler
//
// Recovery is outermost so a panic anywhere below becomes codes.Internal
// instead of crashing the process; Recovery itself logs panics with the stack
// (the "rpc finished" line is skipped for them, while the stats handler still
// records the RPC as Internal). Logging sits outside the error boundary so it
// records the final status code the client actually receives.

// RecoveryUnary converts handler panics into codes.Internal.
func RecoveryUnary(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				err = panicToStatus(ctx, logger, info.FullMethod, r)
			}
		}()
		return handler(ctx, req)
	}
}

// RecoveryStream converts streaming handler panics into codes.Internal.
func RecoveryStream(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = panicToStatus(ss.Context(), logger, info.FullMethod, r)
			}
		}()
		return handler(srv, ss)
	}
}

func panicToStatus(ctx context.Context, logger *slog.Logger, method string, r any) error {
	logger.ErrorContext(ctx, "panic in grpc handler",
		"grpc.method", method, "panic", r, "stack", string(debug.Stack()))
	return status.Error(codes.Internal, "internal error")
}

// LoggingUnary emits one structured line per RPC.
func LoggingUnary(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		logRPC(ctx, logger, info.FullMethod, start, err)
		return resp, err
	}
}

// LoggingStream emits one structured line per stream when it ends.
func LoggingStream(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, ss)
		logRPC(ss.Context(), logger, info.FullMethod, start, err)
		return err
	}
}

func logRPC(ctx context.Context, logger *slog.Logger, method string, start time.Time, err error) {
	code := status.Code(err)
	level := slog.LevelInfo
	if isServerFault(code) {
		level = slog.LevelError
	}
	logger.Log(ctx, level, "rpc finished",
		"grpc.method", method,
		"grpc.code", code.String(),
		"duration_ms", float64(time.Since(start).Microseconds())/1000,
	)
}

// isServerFault separates "we broke" from "caller asked for something
// invalid" so alerts fire on the former only.
func isServerFault(c codes.Code) bool {
	switch c {
	case codes.Unknown, codes.Internal, codes.DataLoss, codes.Unavailable,
		codes.DeadlineExceeded, codes.Unimplemented:
		return true
	default:
		return false
	}
}

// ErrorBoundaryUnary maps handler errors through ToStatus and logs the raw
// error whenever the client-facing status hides a server-side fault.
func ErrorBoundaryUnary(logger *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		return resp, boundary(ctx, logger, info.FullMethod, err)
	}
}

// ErrorBoundaryStream is the streaming counterpart of ErrorBoundaryUnary.
func ErrorBoundaryStream(logger *slog.Logger) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		return boundary(ss.Context(), logger, info.FullMethod, handler(srv, ss))
	}
}

func boundary(ctx context.Context, logger *slog.Logger, method string, err error) error {
	st := ToStatus(err)
	code := status.Code(st)
	_, direct := err.(interface{ GRPCStatus() *status.Status })
	// Internal always deserves the raw error; other server faults only when
	// ToStatus replaced the message (e.g. a wrapped downstream Unavailable).
	if code == codes.Internal || code == codes.Unknown || (!direct && isServerFault(code)) {
		logger.ErrorContext(ctx, "grpc handler error",
			"grpc.method", method, "grpc.code", code.String(), "err", err)
	}
	return st
}
