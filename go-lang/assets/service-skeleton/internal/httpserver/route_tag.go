package httpserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

// RouteTag names the request's span and metrics after the chi route pattern
// ("GET /v1/orders/{id}"), never the raw path. A raw path contains IDs, so
// every distinct ID would become a new span name and metric series: unbounded
// cardinality is a memory leak in the SDK, the collector and the backend.
//
// otelhttp wraps chi from the outside and cannot see the pattern, because chi
// records it on a copy of the request. This middleware runs inside chi and
// reads chi's shared routing context after the handler returns.
func RouteTag(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)

		rctx := chi.RouteContext(r.Context())
		if rctx == nil {
			return
		}
		pattern := rctx.RoutePattern()
		if pattern == "" {
			return // unmatched (404): keep otelhttp's generic span name
		}
		route := semconv.HTTPRoute(pattern)
		span := trace.SpanFromContext(r.Context())
		span.SetName(r.Method + " " + pattern)
		span.SetAttributes(route)
		if labeler, ok := otelhttp.LabelerFromContext(r.Context()); ok {
			labeler.Add(route) // read by otelhttp when it records request metrics
		}
	})
}
