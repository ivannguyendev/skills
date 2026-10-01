//go:build !race

// Allocation budgets live in !race files: the race detector's instrumentation
// changes allocation counts.

package httpserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"example.com/skeleton/internal/httpserver"
)

// discardWriter is a reusable ResponseWriter, so the test measures WriteJSON
// itself rather than httptest's recorder.
type discardWriter struct{ h http.Header }

func (d *discardWriter) Header() http.Header         { return d.h }
func (d *discardWriter) Write(p []byte) (int, error) { return len(p), nil }
func (d *discardWriter) WriteHeader(int)             {}

// Budgets are pinned at the measured value; raising one needs a reason in
// the commit, which is how hot-path regressions get caught in review.
const writeJSONAllocBudget = 3

func TestWriteJSONAllocs(t *testing.T) {
	w := &discardWriter{h: make(http.Header)}
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	v := item{Name: "widget"}
	allocs := testing.AllocsPerRun(1000, func() {
		httpserver.WriteJSON(w, r, http.StatusOK, v)
	})
	if allocs > writeJSONAllocBudget {
		t.Fatalf("WriteJSON allocates %.1f times, budget %d", allocs, writeJSONAllocBudget)
	}
}

func BenchmarkWriteJSON(b *testing.B) {
	w := &discardWriter{h: make(http.Header)}
	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", nil)
	v := item{Name: "widget"}
	b.ReportAllocs()
	for b.Loop() {
		httpserver.WriteJSON(w, r, http.StatusOK, v)
	}
}
