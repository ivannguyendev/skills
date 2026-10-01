package grpcserver_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"example.com/skeleton/internal/apperr"
	"example.com/skeleton/internal/grpcserver"
)

// harness runs a grpcserver.Server on an in-memory listener.
type harness struct {
	conn   *grpc.ClientConn
	cancel context.CancelFunc // triggers the shutdown sequence
	done   chan error         // receives ServeListener's result
}

func start(t *testing.T, cfg grpcserver.Config, fn func(context.Context) error) *harness {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpcserver.New(cfg, slog.New(slog.DiscardHandler))
	registerFake(srv, fn)

	ctx, cancel := context.WithCancel(context.Background())
	h := &harness{cancel: cancel, done: make(chan error, 1)}
	go func() { h.done <- srv.ServeListener(ctx, lis) }()

	// "passthrough:///" skips DNS: bufconn has no real address to resolve.
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	h.conn = conn
	t.Cleanup(func() {
		conn.Close()
		cancel()
		if err := <-h.done; err != nil {
			t.Errorf("ServeListener: %v", err)
		}
	})
	return h
}

func TestHealthReportsServing(t *testing.T) {
	h := start(t, grpcserver.Config{}, func(context.Context) error { return nil })

	for _, svc := range []string{"", "test.v1.Fake"} {
		resp, err := healthpb.NewHealthClient(h.conn).Check(t.Context(), &healthpb.HealthCheckRequest{Service: svc})
		if err != nil {
			t.Fatalf("Check(%q): %v", svc, err)
		}
		if got := resp.GetStatus(); got != healthpb.HealthCheckResponse_SERVING {
			t.Errorf("Check(%q) = %v, want SERVING", svc, got)
		}
	}
}

func TestErrorBoundary(t *testing.T) {
	tests := []struct {
		name    string
		handler func(context.Context) error
		code    codes.Code
		msg     string
	}{
		{"ok", func(context.Context) error { return nil }, codes.OK, ""},
		{"domain error hides wrap text",
			func(context.Context) error {
				return fmt.Errorf("select from orders where id=42: %w", apperr.ErrNotFound)
			},
			codes.NotFound, "not found"},
		{"unexpected error is generic",
			func(context.Context) error { return errors.New("dial tcp 10.0.0.7:5432: connection refused") },
			codes.Internal, "internal error"},
		{"explicit status passes through",
			func(context.Context) error { return status.Error(codes.ResourceExhausted, "quota") },
			codes.ResourceExhausted, "quota"},
		{"panic is recovered",
			func(context.Context) error { panic("boom") },
			codes.Internal, "internal error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := start(t, grpcserver.Config{}, tt.handler)
			st := status.Convert(callFake(t.Context(), h.conn))
			if st.Code() != tt.code || st.Message() != tt.msg {
				t.Errorf("got (%v, %q), want (%v, %q)", st.Code(), st.Message(), tt.code, tt.msg)
			}
		})
	}
}

// A handler that ignores shutdown must not hang the process: GracefulStop
// waits for it, so the server has to fall back to Stop after ShutdownTimeout.
func TestShutdownForcesStopAfterTimeout(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	h := start(t, grpcserver.Config{ShutdownTimeout: 100 * time.Millisecond},
		func(context.Context) error {
			close(entered)
			<-release // deliberately ignores ctx
			return nil
		})
	defer close(release)

	rpcErr := make(chan error, 1)
	go func() { rpcErr <- callFake(context.Background(), h.conn) }()
	<-entered

	begin := time.Now()
	h.cancel()
	select {
	case err := <-rpcErr:
		if status.Code(err) != codes.Unavailable {
			t.Errorf("in-flight RPC got %v, want Unavailable", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not force-stop the stuck RPC")
	}
	if elapsed := time.Since(begin); elapsed < 100*time.Millisecond {
		t.Errorf("stopped after %v, before ShutdownTimeout", elapsed)
	}
}

// Watch streams (used by Envoy and client-side health checking) must not hold
// GracefulStop open: they see NOT_SERVING, then end when draining starts.
func TestWatchStreamEndsOnShutdown(t *testing.T) {
	h := start(t, grpcserver.Config{ShutdownTimeout: 10 * time.Second}, func(context.Context) error { return nil })

	stream, err := healthpb.NewHealthClient(h.conn).Watch(t.Context(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if first, err := stream.Recv(); err != nil || first.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("first update = %v, %v; want SERVING", first, err)
	}

	begin := time.Now()
	h.cancel()
	if next, err := stream.Recv(); err != nil || next.GetStatus() != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("second update = %v, %v; want NOT_SERVING", next, err)
	}
	if _, err := stream.Recv(); err == nil {
		t.Fatal("watch stream still open after drain")
	}
	if err := <-h.done; err != nil {
		t.Fatalf("ServeListener: %v", err)
	}
	h.done <- nil // keep the cleanup's receive satisfied
	if elapsed := time.Since(begin); elapsed > 5*time.Second {
		t.Errorf("shutdown took %v; the watch stream held GracefulStop open", elapsed)
	}
}

// A signal during boot can make shutdown win the race against Serve; that is
// a clean exit, not an error.
func TestShutdownBeforeServeIsClean(t *testing.T) {
	srv := grpcserver.New(grpcserver.Config{}, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := srv.ServeListener(ctx, bufconn.Listen(1<<10)); err != nil {
		t.Fatalf("ServeListener after cancel = %v, want nil", err)
	}
}
