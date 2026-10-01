//go:build !race

// Allocation budgets live in !race files: the race detector's instrumentation
// changes allocation counts.

package resilience_test

import (
	"context"
	"testing"
	"time"

	"example.com/skeleton/internal/resilience"
)

// The limiter sits on every request; a regression here costs allocations on
// the hottest path in the process.
func TestLimiterAllocs(t *testing.T) {
	l := resilience.NewLimiter(64, time.Millisecond)
	ctx := context.Background()
	allocs := testing.AllocsPerRun(1000, func() {
		if err := l.Acquire(ctx); err != nil {
			t.Fatal(err)
		}
		l.Release()
	})
	if allocs != 0 {
		t.Fatalf("Acquire+Release allocates %.1f times, budget is 0", allocs)
	}
}

func BenchmarkLimiterParallel(b *testing.B) {
	l := resilience.NewLimiter(1024, time.Millisecond)
	ctx := context.Background()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := l.Acquire(ctx); err != nil {
				b.Fatal(err)
			}
			l.Release()
		}
	})
}
