package httpserver_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"example.com/skeleton/internal/httpserver"
	"example.com/skeleton/internal/resilience"
)

// settle lets finished goroutines exit, then returns a stable goroutine count.
// It waits on a condition rather than a fixed sleep, so it is not flaky.
func settle() int {
	prev := -1
	for stable := 0; stable < 3; {
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
		n := runtime.NumGoroutine()
		if n == prev {
			stable++
		} else {
			stable = 0
		}
		prev = n
	}
	return prev
}

// Hammer the full chain with every outcome the server produces under stress
// (success, 4xx, 413, panics, shed 503s, client cancellations, deadlines) and
// require the goroutine count to return to its baseline: anything else is a
// per-request leak that would grow with traffic in production.
func TestNoGoroutineLeakUnderLoad(t *testing.T) {
	limiter := resilience.NewLimiter(4, 2*time.Millisecond) // small, so shedding happens
	h := httpserver.NewHandler(slog.New(slog.DiscardHandler), limiter,
		httpserver.HandlerConfig{MaxBodyBytes: 64, Deadline: 20 * time.Millisecond},
		httpserver.Routes{API: routes(make(chan struct{}))})
	base, _ := serve(t, httpserver.Config{}, h)

	client := &http.Client{Timeout: 2 * time.Second}
	defer client.CloseIdleConnections()
	call := func(i int) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		method, path, body := http.MethodGet, "/v1/items/1", ""
		switch i % 6 {
		case 1:
			method, path, body = http.MethodPost, "/v1/items", `{"name":"`+strings.Repeat("x", 200)+`"}`
		case 2:
			path = "/v1/panic"
		case 3:
			path = "/v1/block" // ends by the request deadline
		case 4:
			var c context.CancelFunc
			ctx, c = context.WithTimeout(ctx, time.Millisecond) // client gives up mid-flight
			defer c()
		}
		req, err := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(body))
		if err != nil {
			t.Error(err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return // cancelled on purpose
		}
		_, _ = io.Copy(io.Discard, resp.Body) // drain so the connection is reused
		_ = resp.Body.Close()
	}

	call(0) // warm up the connection pool and lazily started goroutines
	client.CloseIdleConnections()
	baseline := settle()

	var wg sync.WaitGroup
	for w := range 16 {
		wg.Go(func() {
			for i := range 100 {
				call(w*100 + i)
			}
		})
	}
	wg.Wait()
	client.CloseIdleConnections()

	if got := settle(); got > baseline+2 {
		t.Fatalf("goroutines: baseline %d, after load %d — per-request leak", baseline, got)
	}
	if limiter.InFlight() != 0 {
		t.Fatalf("limiter still holds %d slots — a code path skipped Release", limiter.InFlight())
	}
}
