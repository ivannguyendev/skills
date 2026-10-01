package httpserver_test

import (
	"io"
	"log/slog"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"example.com/skeleton/internal/httpserver"
	"example.com/skeleton/internal/resilience"
)

// Streams bypass load shedding and the request deadline: they hold a
// connection for minutes and must not consume an API slot.
func TestStreamsBypassSheddingAndDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := resilience.NewLimiter(1, 0)
		if err := limiter.Acquire(t.Context()); err != nil { // the API budget is exhausted
			t.Fatal(err)
		}
		defer limiter.Release()

		release := make(chan struct{})
		h := httpserver.NewHandler(slog.New(slog.DiscardHandler), limiter,
			httpserver.HandlerConfig{Deadline: 10 * time.Millisecond},
			httpserver.Routes{API: routes(nil), Streams: streamRoutes(release)})
		go func() {
			time.Sleep(time.Minute) // far past the API deadline (fake clock)
			close(release)
		}()
		rec := do(h, http.MethodGet, "/v1/stream", "")
		if rec.Code != http.StatusOK || rec.Body.String() != "stream done" {
			t.Fatalf("stream got %d %q, want 200 \"stream done\"", rec.Code, rec.Body.String())
		}
	})
}

// A panic after the response started must abort the connection rather than
// leave the client with a truncated body that looks like a success.
func TestPanicAfterWriteAbortsResponse(t *testing.T) {
	h := newHandler(t, resilience.NewLimiter(8, 0), nil)
	base, _ := serve(t, httpserver.Config{}, h)
	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/v1/partial", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err == nil {
		_, err = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("client read a complete response; want the connection aborted")
	}
}
