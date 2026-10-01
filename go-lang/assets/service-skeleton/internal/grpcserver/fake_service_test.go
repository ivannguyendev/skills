package grpcserver_test

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// fakeMethod is the full method name served by registerFake.
const fakeMethod = "/test.v1.Fake/Do"

// registerFake registers a one-method unary service whose behaviour is fn.
// It lets server tests exercise interceptors without generated code.
func registerFake(r grpc.ServiceRegistrar, fn func(ctx context.Context) error) {
	r.RegisterService(&grpc.ServiceDesc{
		ServiceName: "test.v1.Fake",
		HandlerType: (*any)(nil),
		Methods: []grpc.MethodDesc{{
			MethodName: "Do",
			Handler: func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
				in := new(emptypb.Empty)
				if err := dec(in); err != nil {
					return nil, err
				}
				h := func(ctx context.Context, _ any) (any, error) {
					if err := fn(ctx); err != nil {
						return nil, err
					}
					return &emptypb.Empty{}, nil
				}
				if interceptor == nil {
					return h(ctx, in)
				}
				return interceptor(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: fakeMethod}, h)
			},
		}},
	}, struct{}{})
}

// callFake invokes the fake method over conn.
func callFake(ctx context.Context, conn *grpc.ClientConn) error {
	return conn.Invoke(ctx, fakeMethod, &emptypb.Empty{}, &emptypb.Empty{})
}
