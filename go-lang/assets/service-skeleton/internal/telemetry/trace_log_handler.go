package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// TraceHandler decorates a slog.Handler with trace_id and span_id taken from
// the context passed to the *Context logging methods (InfoContext, ...).
// That pair is what lets a log backend jump straight to the matching trace.
// Logging without a context (slog.Info) cannot be correlated.
//
// The IDs are record attributes, so a logger derived with WithGroup nests
// them inside that group; keep correlation-critical loggers group-free.
type TraceHandler struct {
	slog.Handler
}

// NewTraceHandler wraps next, e.g. NewTraceHandler(slog.NewJSONHandler(os.Stdout, nil)).
func NewTraceHandler(next slog.Handler) *TraceHandler {
	return &TraceHandler{Handler: next}
}

// Handle adds the IDs only when the context carries a valid span.
func (h *TraceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r = r.Clone() // Records share attr storage; never mutate the caller's
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs keeps the decorator in place for derived loggers.
func (h *TraceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceHandler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup keeps the decorator in place for derived loggers.
func (h *TraceHandler) WithGroup(name string) slog.Handler {
	return &TraceHandler{Handler: h.Handler.WithGroup(name)}
}
