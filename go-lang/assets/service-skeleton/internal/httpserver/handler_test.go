package httpserver_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"example.com/skeleton/internal/httpserver"
	"example.com/skeleton/internal/resilience"
)

type item struct {
	Name string `json:"name"`
}

// routes is a tiny API used to exercise the full middleware chain.
func routes(release <-chan struct{}) func(chi.Router) {
	return func(r chi.Router) {
		r.Get("/v1/items/{id}", func(w http.ResponseWriter, r *http.Request) {
			httpserver.WriteJSON(w, r, http.StatusOK, item{Name: chi.URLParam(r, "id")})
		})
		r.Post("/v1/items", func(w http.ResponseWriter, r *http.Request) {
			var in item
			if !httpserver.DecodeJSON(w, r, &in) {
				return
			}
			httpserver.WriteJSON(w, r, http.StatusCreated, in)
		})
		r.Get("/v1/panic", func(http.ResponseWriter, *http.Request) { panic("boom") })
		r.Get("/v1/partial", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "half a response")
			panic("boom after writing")
		})
		r.Get("/v1/block", func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		})
	}
}

// streamRoutes registers a long-lived route that only ends on release or
// when the client goes away.
func streamRoutes(release <-chan struct{}) func(chi.Router) {
	return func(r chi.Router) {
		r.Get("/v1/stream", func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
				_, _ = io.WriteString(w, "stream done")
			case <-r.Context().Done():
			}
		})
	}
}

func newHandler(t *testing.T, limiter *resilience.Limiter, tp *sdktrace.TracerProvider) http.Handler {
	t.Helper()
	cfg := httpserver.HandlerConfig{MaxBodyBytes: 64}
	if tp != nil {
		cfg.OTel = []otelhttp.Option{otelhttp.WithTracerProvider(tp)}
	}
	return httpserver.NewHandler(slog.New(slog.DiscardHandler), limiter, cfg, httpserver.Routes{API: routes(nil)})
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestChainStatusCodes(t *testing.T) {
	h := newHandler(t, resilience.NewLimiter(8, 0), nil)
	tests := []struct {
		name, method, path, body string
		want                     int
	}{
		{"ok", http.MethodGet, "/v1/items/42", "", http.StatusOK},
		{"create", http.MethodPost, "/v1/items", `{"name":"a"}`, http.StatusCreated},
		{"unknown field", http.MethodPost, "/v1/items", `{"nme":"a"}`, http.StatusBadRequest},
		{"two values", http.MethodPost, "/v1/items", `{"name":"a"}{}`, http.StatusBadRequest},
		{"too large", http.MethodPost, "/v1/items", `{"name":"` + strings.Repeat("x", 100) + `"}`, http.StatusRequestEntityTooLarge},
		{"panic", http.MethodGet, "/v1/panic", "", http.StatusInternalServerError},
		{"not found", http.MethodGet, "/nope", "", http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := do(h, tt.method, tt.path, tt.body).Code; got != tt.want {
				t.Errorf("status = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestLoadShedReturns503WithRetryAfter(t *testing.T) {
	limiter := resilience.NewLimiter(1, 0)
	if err := limiter.Acquire(t.Context()); err != nil { // occupy the only slot
		t.Fatal(err)
	}
	defer limiter.Release()

	h := newHandler(t, limiter, nil)
	rec := do(h, http.MethodGet, "/v1/items/1", "")
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("got %d Retry-After=%q, want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}

	// A client that gives up while queued is not a server error.
	queued := resilience.NewLimiter(1, time.Hour)
	if err := queued.Acquire(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer queued.Release()
	waiting := httpserver.NewHandler(slog.New(slog.DiscardHandler), queued,
		httpserver.HandlerConfig{}, httpserver.Routes{API: routes(nil)})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rec = httptest.NewRecorder()
	waiting.ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/v1/items/1", nil))
	if rec.Code != 499 {
		t.Fatalf("cancelled client got %d, want 499 (client closed request)", rec.Code)
	}
}

// Span names must come from the route pattern, never the raw path.
func TestSpanNamedAfterRoutePattern(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	do(newHandler(t, resilience.NewLimiter(8, 0), tp), http.MethodGet, "/v1/items/42", "")
	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Name() != "GET /v1/items/{id}" {
		names := make([]string, 0, len(spans))
		for _, s := range spans {
			names = append(names, s.Name())
		}
		t.Fatalf("span names = %v, want [GET /v1/items/{id}]", names)
	}
}

func TestDeadlineReachesHandler(t *testing.T) {
	h := httpserver.NewHandler(slog.New(slog.DiscardHandler), resilience.NewLimiter(8, 0),
		httpserver.HandlerConfig{Deadline: 50 * time.Millisecond}, httpserver.Routes{API: routes(make(chan struct{}))})
	start := time.Now()
	rec := do(h, http.MethodGet, "/v1/block", "")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("handler ran %v; request deadline not applied", elapsed)
	}
	_, _ = io.Copy(io.Discard, rec.Body)
}
