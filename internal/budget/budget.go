// Package budget bounds active work and its waiting queue.
package budget

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

var ErrOverloaded = errors.New("work budget exhausted")

type Limiter struct {
	slots, queue chan struct{}
	rejected     atomic.Uint64
}
type Stats struct {
	InFlight, Queued int
	Rejected         uint64
}

func New(active, queued int) *Limiter {
	return &Limiter{slots: make(chan struct{}, active), queue: make(chan struct{}, queued)}
}
func (l *Limiter) Acquire(ctx context.Context, wait time.Duration) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case l.slots <- struct{}{}:
		return func() { <-l.slots }, nil
	default:
	}
	select {
	case l.queue <- struct{}{}:
		defer func() { <-l.queue }()
	default:
		l.rejected.Add(1)
		return nil, ErrOverloaded
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case l.slots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-l.slots
			return nil, err
		}
		return func() { <-l.slots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		l.rejected.Add(1)
		return nil, ErrOverloaded
	}
}
func (l *Limiter) Stats() Stats { return Stats{len(l.slots), len(l.queue), l.rejected.Load()} }
