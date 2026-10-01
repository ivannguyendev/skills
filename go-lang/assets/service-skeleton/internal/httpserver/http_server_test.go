package httpserver_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"go.uber.org/goleak"

	"example.com/skeleton/internal/httpserver"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// serve runs h on a loopback port and returns its base URL. Cleanup shuts the
// server down and fails the test if ServeListener reports an error.
func serve(t *testing.T, cfg httpserver.Config, h http.Handler) (string, context.CancelFunc) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	srv := httpserver.New(cfg, h, slog.New(slog.DiscardHandler))
	go func() { done <- srv.ServeListener(ctx, lis) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("ServeListener: %v", err)
		}
	})
	return "http://" + lis.Addr().String(), cancel
}

// A client that never finishes its headers must be disconnected after
// ReadHeaderTimeout instead of pinning a goroutine and a socket forever.
func TestSlowHeadersAreCutOff(t *testing.T) {
	base, _ := serve(t, httpserver.Config{ReadHeaderTimeout: 100 * time.Millisecond},
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))

	conn, err := net.Dial("tcp", base[len("http://"):])
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\n"); err != nil { // no final CRLF
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, _ = io.ReadAll(conn) // returns when the server closes the connection
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("connection held for %v; ReadHeaderTimeout not applied", elapsed)
	}
}

// Shutdown must let an in-flight request finish before ServeListener returns.
func TestGracefulShutdownFinishesInFlight(t *testing.T) {
	started := make(chan struct{})
	base, stop := serve(t, httpserver.Config{ShutdownTimeout: 5 * time.Second},
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			close(started)
			time.Sleep(100 * time.Millisecond)
			_, _ = io.WriteString(w, "done")
		}))

	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	result := make(chan string, 1)
	go func() {
		resp, err := client.Get(base) //nolint:noctx // test client with its own timeout
		if err != nil {
			result <- err.Error()
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		result <- string(body)
	}()

	<-started
	stop()
	if got := <-result; got != "done" {
		t.Fatalf("in-flight request got %q, want it to complete during shutdown", got)
	}
}
