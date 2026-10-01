package grpcclient

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
)

// wrapDownstreamUnary wraps every error a downstream call returns.
//
// A handler that does `return nil, err` after a failed call would otherwise
// hand the dependency's status straight to its own caller: the downstream
// InvalidArgument (really our bug) and its message would be forwarded verbatim.
// Wrapped, the error is no longer a "direct" status, so the server's error
// boundary (grpcserver.ToStatus) sanitises it. status.Code and status.Convert
// still see the original code through the wrap, so client code is unaffected.
func wrapDownstreamUnary(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	if err := invoker(ctx, method, req, reply, cc, opts...); err != nil {
		return fmt.Errorf("call %s: %w", method, err)
	}
	return nil
}
