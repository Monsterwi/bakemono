package server

import (
	"context"
	"sync"
	"sync/atomic"
)

const responseBufferSize = 256 << 10

type responseBuffer struct{ data []byte }

// The lease cap bounds active payload; idle sync.Pool entries remain GC-managed.
// The usual path preserves per-P reuse without a shared free-list lock.
type responseBufferPool struct {
	buffers        sync.Pool
	capacity       int64
	totalAllocated atomic.Int64
	inUse          atomic.Int64
	mu             sync.Mutex
	changed        chan struct{}
	waiters        atomic.Int64
}

func newResponseBufferPool(requests int) *responseBufferPool {
	p := &responseBufferPool{capacity: int64(requests) * responseBufferSize, changed: make(chan struct{})}
	p.buffers.New = func() any {
		p.totalAllocated.Add(responseBufferSize)
		return &responseBuffer{data: make([]byte, responseBufferSize)}
	}
	return p
}

func (p *responseBufferPool) acquire(ctx context.Context) (*responseBuffer, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		active := p.inUse.Load()
		if active < p.capacity {
			if p.inUse.CompareAndSwap(active, active+responseBufferSize) {
				return p.buffers.Get().(*responseBuffer), nil
			}
			continue
		}
		p.mu.Lock()
		// Register under the notification lock and recheck after a racing release.
		if p.inUse.Load() < p.capacity {
			p.mu.Unlock()
			continue
		}
		changed := p.changed
		p.waiters.Add(1)
		p.mu.Unlock()
		var err error
		select {
		case <-changed:
		case <-ctx.Done():
			err = ctx.Err()
		}
		p.waiters.Add(-1)
		if err != nil {
			return nil, err
		}
	}
}

func (p *responseBufferPool) release(buffer *responseBuffer) {
	p.buffers.Put(buffer)
	p.inUse.Add(-responseBufferSize)
	if p.waiters.Load() > 0 {
		p.mu.Lock()
		close(p.changed)
		p.changed = make(chan struct{})
		p.mu.Unlock()
	}
}
