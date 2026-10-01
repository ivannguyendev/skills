package admin_test

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"go.uber.org/goleak"

	"example.com/skeleton/internal/admin"
	"example.com/skeleton/internal/httpserver"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestProbes(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := admin.New(httpserver.Config{}, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.ServeListener(ctx, lis) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("ServeListener: %v", err)
		}
	})

	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	status := func(path string) int {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+lis.Addr().String()+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if got := status("/livez"); got != http.StatusOK {
		t.Errorf("/livez = %d, want 200", got)
	}
	if got := status("/readyz"); got != http.StatusServiceUnavailable {
		t.Errorf("/readyz before SetReady = %d, want 503", got)
	}
	srv.SetReady(true)
	if got := status("/readyz"); got != http.StatusOK {
		t.Errorf("/readyz after SetReady = %d, want 200", got)
	}
	if got := status("/debug/pprof/goroutine?debug=1"); got != http.StatusOK {
		t.Errorf("pprof goroutine = %d, want 200", got)
	}
}
