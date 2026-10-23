package server

import (
	"context"
	"sync/atomic"
)

const responseBufferSize = 256 << 10

// A runtime retains at most one buffer per admitted request, even across GC.
// Idle buffers are reused before atomically reserving another allocation.
type responseBufferPool struct {
	buffers   chan []byte
	allocated atomic.Int64
	inUse     atomic.Int64
}

func newResponseBufferPool(requests int) *responseBufferPool {
	return &responseBufferPool{buffers: make(chan []byte, requests)}
}

func (p *responseBufferPool) acquire(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case buffer := <-p.buffers:
		p.inUse.Add(responseBufferSize)
		return buffer, nil
	default:
	}
	limit := int64(cap(p.buffers)) * responseBufferSize
	for allocated := p.allocated.Load(); allocated < limit; allocated = p.allocated.Load() {
		if p.allocated.CompareAndSwap(allocated, allocated+responseBufferSize) {
			buffer := make([]byte, responseBufferSize)
			p.inUse.Add(responseBufferSize)
			return buffer, nil
		}
	}
	select {
	case buffer := <-p.buffers:
		if err := ctx.Err(); err != nil {
			p.buffers <- buffer
			return nil, err
		}
		p.inUse.Add(responseBufferSize)
		return buffer, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *responseBufferPool) release(buffer []byte) {
	p.inUse.Add(-responseBufferSize)
	p.buffers <- buffer
}
