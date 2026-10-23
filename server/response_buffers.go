package server

import (
	"context"
	"sync"
	"sync/atomic"
)

const responseBufferSize = 256 << 10

// Runtime-owned buffers have a strict payload cap and survive GC.
// LIFO reuse favors recently touched data; no body allocation or I/O holds mu.
type responseBufferPool struct {
	mu        sync.Mutex
	buffers   [][]byte
	capacity  int64
	allocated atomic.Int64
	inUse     atomic.Int64
	changed   chan struct{}
	waiters   int
}

func newResponseBufferPool(requests int) *responseBufferPool {
	return &responseBufferPool{capacity: int64(requests) * responseBufferSize, changed: make(chan struct{})}
}
func (p *responseBufferPool) acquire(ctx context.Context) ([]byte, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p.mu.Lock()
		if n := len(p.buffers); n > 0 {
			b := p.buffers[n-1]
			p.buffers[n-1] = nil
			p.buffers = p.buffers[:n-1]
			p.inUse.Add(responseBufferSize)
			p.mu.Unlock()
			return b, nil
		}
		if p.allocated.Load() < p.capacity {
			p.allocated.Add(responseBufferSize)
			p.inUse.Add(responseBufferSize)
			p.mu.Unlock()
			return make([]byte, responseBufferSize), nil
		}
		changed := p.changed
		p.waiters++
		p.mu.Unlock()
		var err error
		select {
		case <-changed:
		case <-ctx.Done():
			err = ctx.Err()
		}
		p.mu.Lock()
		p.waiters--
		p.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
}
func (p *responseBufferPool) release(b []byte) {
	p.mu.Lock()
	p.inUse.Add(-responseBufferSize)
	p.buffers = append(p.buffers, b)
	if p.waiters > 0 {
		close(p.changed)
		p.changed = make(chan struct{})
	}
	p.mu.Unlock()
}
