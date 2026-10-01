package telemetry_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/trace"

	"example.com/skeleton/internal/telemetry"
)

func TestTraceHandlerAddsIDs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(telemetry.NewTraceHandler(slog.NewJSONHandler(&buf, nil))).With("svc", "orders")

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x0a, 0xf7, 0x65, 0x19, 0x16, 0xcd, 0x43, 0xdd, 0x84, 0x48, 0xeb, 0x21, 0x1c, 0x80, 0x31, 0x9c},
		SpanID:     trace.SpanID{0xb7, 0xad, 0x6b, 0x71, 0x69, 0x20, 0x33, 0x31},
		TraceFlags: trace.FlagsSampled,
	})
	logger.InfoContext(trace.ContextWithSpanContext(context.Background(), sc), "hello")
	logger.InfoContext(context.Background(), "no span")

	dec := json.NewDecoder(&buf)
	var withSpan, without map[string]any
	if err := dec.Decode(&withSpan); err != nil {
		t.Fatal(err)
	}
	if err := dec.Decode(&without); err != nil {
		t.Fatal(err)
	}

	if got := withSpan["trace_id"]; got != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("trace_id = %v", got)
	}
	if got := withSpan["span_id"]; got != "b7ad6b7169203331" {
		t.Errorf("span_id = %v", got)
	}
	if withSpan["svc"] != "orders" {
		t.Errorf("WithAttrs lost the decorator: %v", withSpan)
	}
	if _, ok := without["trace_id"]; ok {
		t.Errorf("trace_id present without a span: %v", without)
	}
}
