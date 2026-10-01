package main

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func testConfig(t *testing.T) config {
	t.Helper()
	t.Setenv("HTTP_ADDR", "127.0.0.1:0")
	t.Setenv("GRPC_ADDR", "127.0.0.1:0")
	t.Setenv("ADMIN_ADDR", "127.0.0.1:0")
	cfg, err := loadConfig(t.Context(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// The full process starts, stops on cancellation, and leaves no goroutines
// behind (goleak checks after all tests).
func TestServeStartsAndStopsCleanly(t *testing.T) {
	cfg := testConfig(t)
	logger := slog.New(slog.DiscardHandler)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, logger, cfg, newServers(cfg, logger)) }()

	// serve binds its listeners synchronously, so cancelling right away still
	// exercises the full shutdown path, including servers that race Serve.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not return after cancellation")
	}
}

func TestConfigRejectsBrokenTimeoutHierarchy(t *testing.T) {
	tests := []struct {
		name, key, value, wantErr string
	}{
		{"deadline above write timeout", "HTTP_REQUEST_DEADLINE", "20s", "HTTP_REQUEST_DEADLINE"},
		{"budget too small", "SHUTDOWN_BUDGET", "5s", "SHUTDOWN_BUDGET"},
		{"partial mTLS", "TLS_CERT_FILE", "/tmp/cert.pem", "mtls"},
		{"bad duration", "QUEUE_WAIT", "fast", "QUEUE_WAIT"},
		{"no capacity", "MAX_INFLIGHT", "0", "MAX_INFLIGHT"},
		{"queue wait too long", "QUEUE_WAIT", "2s", "QUEUE_WAIT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			_, err := loadConfig(t.Context(), slog.New(slog.DiscardHandler))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("loadConfig error = %v, want it to mention %s", err, tt.wantErr)
			}
		})
	}
}
