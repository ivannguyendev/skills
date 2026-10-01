package resilience_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"go.uber.org/goleak"

	"example.com/skeleton/internal/resilience"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestLimiterRejectsAfterQueueWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := resilience.NewLimiter(1, 25*time.Millisecond)
		if err := l.Acquire(t.Context()); err != nil {
			t.Fatal(err)
		}

		start := time.Now()
		err := l.Acquire(t.Context())
		if !errors.Is(err, resilience.ErrOverloaded) {
			t.Fatalf("second Acquire = %v, want ErrOverloaded", err)
		}
		if waited := time.Since(start); waited != 25*time.Millisecond {
			t.Errorf("waited %v, want exactly the queue wait", waited)
		}
		if l.Rejected() != 1 || l.InFlight() != 1 {
			t.Errorf("rejected=%d inflight=%d, want 1/1", l.Rejected(), l.InFlight())
		}
		l.Release()
	})
}

func TestLimiterWaiterGetsReleasedSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := resilience.NewLimiter(1, time.Second)
		if err := l.Acquire(t.Context()); err != nil {
			t.Fatal(err)
		}
		go func() {
			time.Sleep(10 * time.Millisecond)
			l.Release()
		}()
		if err := l.Acquire(t.Context()); err != nil {
			t.Fatalf("waiter: %v", err)
		}
		l.Release()
	})
}

func TestLimiterHonoursCallerCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := resilience.NewLimiter(1, time.Hour)
		if err := l.Acquire(t.Context()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Millisecond)
		defer cancel()
		if err := l.Acquire(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Acquire = %v, want DeadlineExceeded", err)
		}
		if l.Rejected() != 0 {
			t.Error("a caller that gave up is not a rejection")
		}
		l.Release()
	})
}

func TestLimiterZeroWaitRejectsImmediately(t *testing.T) {
	l := resilience.NewLimiter(1, 0)
	if err := l.Acquire(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	if err := l.Acquire(t.Context()); !errors.Is(err, resilience.ErrOverloaded) {
		t.Fatalf("Acquire = %v, want ErrOverloaded", err)
	}
}
