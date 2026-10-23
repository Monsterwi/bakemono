package server

import (
	"context"
	"math/rand/v2"
	"sync/atomic"
)

const responseBufferSize = 256 << 10

// A runtime retains at most one buffer per admitted request, even across GC.
// Shards avoid serializing every response on a single channel's lock.
type responseBufferPool struct {
	buffers   [8]chan []byte
	capacity  int64
	allocated atomic.Int64
	inUse     atomic.Int64
}

func newResponseBufferPool(requests int) *responseBufferPool {
	p := &responseBufferPool{capacity: int64(requests) * responseBufferSize}
	for i := range p.buffers {
		p.buffers[i] = make(chan []byte, requests)
	}
	return p
}

func (p *responseBufferPool) take(start int) []byte {
	for i := range p.buffers {
		select {
		case buffer := <-p.buffers[(start+i)%len(p.buffers)]:
			return buffer
		default:
		}
	}
	return nil
}

func (p *responseBufferPool) acquire(ctx context.Context) ([]byte, error) {
	start := rand.IntN(len(p.buffers))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if buffer := p.take(start); buffer != nil {
			p.inUse.Add(responseBufferSize)
			return buffer, nil
		}
		for allocated := p.allocated.Load(); allocated < p.capacity; allocated = p.allocated.Load() {
			if p.allocated.CompareAndSwap(allocated, allocated+responseBufferSize) {
				buffer := make([]byte, responseBufferSize)
				p.inUse.Add(responseBufferSize)
				return buffer, nil
			}
		}
		var buffer []byte
		select {
		case buffer = <-p.buffers[0]:
		case buffer = <-p.buffers[1]:
		case buffer = <-p.buffers[2]:
		case buffer = <-p.buffers[3]:
		case buffer = <-p.buffers[4]:
		case buffer = <-p.buffers[5]:
		case buffer = <-p.buffers[6]:
		case buffer = <-p.buffers[7]:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		p.inUse.Add(responseBufferSize)
		if err := ctx.Err(); err != nil {
			p.release(buffer)
			return nil, err
		}
		return buffer, nil
	}
}

func (p *responseBufferPool) release(buffer []byte) {
	p.inUse.Add(-responseBufferSize)
	p.buffers[rand.IntN(len(p.buffers))] <- buffer
}
