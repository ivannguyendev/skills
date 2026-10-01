package httpserver

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5"

	"example.com/skeleton/internal/resilience"
)

// Middleware is the standard net/http decorator shape (what chi's Use takes).
type Middleware = func(http.Handler) http.Handler

// statusClientClosedRequest follows the nginx convention for "the client went
// away before we answered": not a server error, so it never pages anyone.
const statusClientClosedRequest = 499

// Recover turns a handler panic into a 500 and logs the stack, so one bad
// request cannot take the process down. If the response has already started,
// a 500 can no longer be sent, so it aborts the connection instead: the client
// then sees a broken response, not a truncated one that looks successful.
// http.ErrAbortHandler is re-raised; net/http handles it silently by design.
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				logger.ErrorContext(r.Context(), "panic in http handler",
					"panic", v, "stack", string(debug.Stack()))
				if sw, ok := w.(*statusWriter); ok && sw.wrote {
					panic(http.ErrAbortHandler)
				}
				http.Error(w, "internal error", http.StatusInternalServerError)
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// LoadShed rejects work when the shared limiter has no slot within its queue
// wait. 503 + Retry-After tells clients and load balancers to go elsewhere;
// queueing instead would grow memory and latency until everything times out.
func LoadShed(l *resilience.Limiter) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := l.Acquire(r.Context()); err != nil {
				if errors.Is(err, resilience.ErrOverloaded) {
					w.Header().Set("Retry-After", "1")
					http.Error(w, "server overloaded", http.StatusServiceUnavailable)
					return
				}
				w.WriteHeader(statusClientClosedRequest) // client gave up while queued
				return
			}
			defer l.Release()
			next.ServeHTTP(w, r)
		})
	}
}

// BodyLimit caps request bodies; reads past the limit fail with
// *http.MaxBytesError (DecodeJSON maps it to 413) instead of buffering an
// attacker-chosen amount of memory.
func BodyLimit(maxBytes int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// Deadline gives every request a context deadline, so downstream calls made
// with r.Context() stop when the budget is spent. It is preferred over
// http.TimeoutHandler, which buffers whole responses and breaks streaming.
func Deadline(d time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// AccessLog writes one line per request with the route pattern (never the raw
// path, which carries IDs). With errorsOnly (the high-load default) it logs
// only 5xx and requests slower than slow; logging every request serializes on
// the log writer and costs allocations at peak.
func AccessLog(logger *slog.Logger, errorsOnly bool, slow time.Duration) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			elapsed := time.Since(start)
			if errorsOnly && sw.status < 500 && elapsed < slow {
				return
			}
			level := slog.LevelInfo
			if sw.status >= 500 {
				level = slog.LevelError
			}
			route := ""
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				route = rctx.RoutePattern()
			}
			logger.LogAttrs(r.Context(), level, "http request",
				slog.String("http.method", r.Method),
				slog.String("http.route", route),
				slog.Int("http.status", sw.status),
				slog.Duration("duration", elapsed))
		})
	}
}

// statusWriter records the first final status code. It forwards Flush and
// Hijack so streaming and WebSocket handlers keep working behind it, and
// Unwrap keeps http.ResponseController (deadlines) working too.
type statusWriter struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote && code >= 200 { // 1xx (e.g. 103 Early Hints) is not the final status
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	w.wrote = true
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Flush() { _ = http.NewResponseController(w.ResponseWriter).Flush() }

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
