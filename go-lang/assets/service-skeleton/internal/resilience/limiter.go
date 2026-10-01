// Package resilience provides overload protection shared by every transport.
package resilience

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

// ErrOverloaded means no slot freed up within the queue wait. Transports map
// it to HTTP 503 + Retry-After or gRPC UNAVAILABLE, which clients and load
// balancers treat as "try another replica".
var ErrOverloaded = errors.New("resilience: overloaded")

// Limiter caps concurrent work for the whole process (HTTP and gRPC share
// one instance, so neither transport can starve the other).
//
// Under overload, queued requests would otherwise pile up: every waiter holds
// a goroutine, its request memory and a client connection, and latency grows
// until everything times out at once. Bounding both concurrency and queue
// wait turns overload into fast, cheap rejections instead.
//
// Size it with Little's law: maxInFlight ≈ peak RPS × p99 latency × 2–3.
type Limiter struct {
	slots     chan struct{}
	queueWait time.Duration
	rejected  atomic.Int64
}

// NewLimiter allows maxInFlight concurrent holders; a caller waits at most
// queueWait for a slot (0 means reject immediately when full).
func NewLimiter(maxInFlight int, queueWait time.Duration) *Limiter {
	return &Limiter{
		slots:     make(chan struct{}, max(maxInFlight, 1)),
		queueWait: max(queueWait, 0),
	}
}

// Acquire takes a slot. On success the caller must call Release exactly once,
// typically with defer. It returns ErrOverloaded when the queue wait elapses
// and ctx's error when the caller gives up first.
//
// The uncontended path does not allocate; only callers that have to wait
// create a timer.
func (l *Limiter) Acquire(ctx context.Context) error {
	select {
	case l.slots <- struct{}{}:
		return nil
	default:
	}
	if l.queueWait == 0 {
		l.rejected.Add(1)
		return ErrOverloaded
	}

	t := time.NewTimer(l.queueWait)
	defer t.Stop()
	select {
	case l.slots <- struct{}{}:
		return nil
	case <-t.C:
		l.rejected.Add(1)
		return ErrOverloaded
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Release frees a slot taken by a successful Acquire. Releasing without a
// matching Acquire is a programming error. When other slots are held it
// silently frees one of theirs (over-admitting from then on), so pair every
// Acquire with a deferred Release. When no slot is held it would block
// forever, so it panics instead.
func (l *Limiter) Release() {
	select {
	case <-l.slots:
	default:
		panic("resilience: Release without a matching Acquire")
	}
}

// InFlight reports how many slots are held (export it as a gauge).
func (l *Limiter) InFlight() int { return len(l.slots) }

// Capacity reports the configured maximum.
func (l *Limiter) Capacity() int { return cap(l.slots) }

// Rejected reports how many callers were shed (export it as a counter).
func (l *Limiter) Rejected() int64 { return l.rejected.Load() }
