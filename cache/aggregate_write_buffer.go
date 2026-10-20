package cache

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

const (
	AggBufferSize    = 4 * 1024 * 1024
	AggHighWaterMark = AggBufferSize / 2
)

type PendingWriter interface{ OnLogFlush(error) }

// FlushTask owns its bytes until NotifyFlushDone. Allocation, including wrap,
// happens once in Add; the disk worker must use this exact offset and generation.
type FlushTask struct {
	Buffer     []byte
	Writers    []PendingWriter
	Offset     int64
	Generation uint64
}

type allocation struct {
	offset               int64
	generation, sequence uint64
}

var ErrCacheClosed = errors.New("cache storage closed")

type AggregateWriteBuffer struct {
	buffers                         [2][]byte
	bufferPos                       [2]int
	bufferStart                     [2]int64
	bufferGeneration                [2]uint64
	bufferSequence                  [2]uint64
	pendingWriters                  [2][]PendingWriter
	activeIdx                       int
	writeCursor                     int64
	generation, sequence, completed uint64
	mu                              sync.Mutex
	changed                         chan struct{}
	flushChan                       chan FlushTask
	isFlushing                      bool
	minOffset, maxOffset            int64
	err                             error
	closed                          bool
}

func NewAggregateWriteBuffer() *AggregateWriteBuffer {
	return &AggregateWriteBuffer{buffers: [2][]byte{make([]byte, AggBufferSize), make([]byte, AggBufferSize)}, changed: make(chan struct{}), flushChan: make(chan FlushTask, 1)}
}

func (b *AggregateWriteBuffer) Init(offset int64) { b.initPosition(offset, false) }
func (b *AggregateWriteBuffer) initPosition(offset int64, phase bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writeCursor = offset
	if b.maxOffset == 0 {
		b.minOffset = offset
	}
	if phase {
		b.generation = 1
	} else {
		b.generation = 0
	}
}
func (b *AggregateWriteBuffer) SetBounds(min, max int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.minOffset, b.maxOffset = min, max
	if b.writeCursor < min {
		b.writeCursor = min
	}
}
func (b *AggregateWriteBuffer) IsEmpty() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bufferPos[b.activeIdx] == 0 && !b.isFlushing
}
func (b *AggregateWriteBuffer) GetBufferPos() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bufferPos[b.activeIdx]
}
func (b *AggregateWriteBuffer) GetWriteCursor() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writeCursor
}

// Stats counts bounded aggregate bytes, including the buffer owned by disk I/O.
func (b *AggregateWriteBuffer) Stats() (queued int, bufferedBytes int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	bufferedBytes = int64(b.bufferPos[b.activeIdx])
	if bufferedBytes > 0 {
		queued++
	}
	if b.isFlushing {
		queued++
		bufferedBytes += int64(b.bufferPos[1-b.activeIdx])
	}
	return
}
func (b *AggregateWriteBuffer) Err() error    { b.mu.Lock(); defer b.mu.Unlock(); return b.err }
func (b *AggregateWriteBuffer) signalLocked() { close(b.changed); b.changed = make(chan struct{}) }
func (b *AggregateWriteBuffer) waitLocked(ctx context.Context) error {
	ch := b.changed
	b.mu.Unlock()
	select {
	case <-ch:
	case <-ctx.Done():
	}
	b.mu.Lock()
	return ctx.Err()
}
func (b *AggregateWriteBuffer) Add(data []byte, size int, writer PendingWriter) (int64, error) {
	return b.AddContext(context.Background(), data, size, writer)
}
func (b *AggregateWriteBuffer) AddContext(ctx context.Context, data []byte, size int, writer PendingWriter) (int64, error) {
	a, err := b.addContext(ctx, data, size, writer)
	return a.offset, err
}
func (b *AggregateWriteBuffer) addContext(ctx context.Context, data []byte, _ int, writer PendingWriter) (allocation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(data) == 0 || len(data) > AggBufferSize || (b.maxOffset != 0 && (b.maxOffset <= b.minOffset || int64(len(data)) > b.maxOffset-b.minOffset)) {
		return allocation{}, fmt.Errorf("record does not fit aggregate or stripe")
	}
	for {
		if b.err != nil {
			return allocation{}, b.err
		}
		if b.closed {
			return allocation{}, ErrCacheClosed
		}
		if err := ctx.Err(); err != nil {
			return allocation{}, err
		}
		if b.maxOffset > 0 && b.writeCursor+int64(len(data)) > b.maxOffset {
			if b.isFlushing {
				if err := b.waitLocked(ctx); err != nil {
					return allocation{}, err
				}
				continue
			}
			b.swapAndFlushLocked()
			b.writeCursor = b.minOffset
			b.generation++
			continue
		}
		if b.bufferPos[b.activeIdx]+len(data) <= AggBufferSize {
			break
		}
		if !b.isFlushing {
			b.swapAndFlushLocked()
			continue
		}
		if err := b.waitLocked(ctx); err != nil {
			return allocation{}, err
		}
	}
	idx, pos := b.activeIdx, b.bufferPos[b.activeIdx]
	if pos == 0 {
		b.bufferStart[idx] = b.writeCursor
		b.bufferGeneration[idx] = b.generation
	}
	copy(b.buffers[idx][pos:], data)
	b.bufferPos[idx] += len(data)
	if writer != nil {
		b.pendingWriters[idx] = append(b.pendingWriters[idx], writer)
	}
	b.sequence++
	b.bufferSequence[idx] = b.sequence
	a := allocation{b.writeCursor, b.generation, b.sequence}
	b.writeCursor += int64(len(data))
	if b.bufferPos[idx] >= AggHighWaterMark && !b.isFlushing {
		b.swapAndFlushLocked()
	}
	return a, nil
}
func (b *AggregateWriteBuffer) swapAndFlushLocked() {
	idx := b.activeIdx
	if b.isFlushing || b.bufferPos[idx] == 0 || b.err != nil {
		return
	}
	task := FlushTask{Buffer: b.buffers[idx][:b.bufferPos[idx]], Writers: b.pendingWriters[idx], Offset: b.bufferStart[idx], Generation: b.bufferGeneration[idx]}
	b.isFlushing = true
	b.activeIdx = 1 - idx
	b.bufferPos[b.activeIdx] = 0
	b.pendingWriters[b.activeIdx] = nil
	b.flushChan <- task
}
func (b *AggregateWriteBuffer) TriggerFlush() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.swapAndFlushLocked()
}
func (b *AggregateWriteBuffer) FlushSync() error { return b.FlushContext(context.Background()) }

// FlushContext is a barrier for additions preceding this call, not future traffic.
func (b *AggregateWriteBuffer) FlushContext(ctx context.Context) error {
	b.mu.Lock()
	target := b.sequence
	b.mu.Unlock()
	return b.flushThrough(ctx, target)
}
func (b *AggregateWriteBuffer) flushThrough(ctx context.Context, target uint64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for {
		if b.err != nil {
			return b.err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if b.completed >= target {
			return nil
		}
		b.swapAndFlushLocked()
		if err := b.waitLocked(ctx); err != nil {
			return err
		}
	}
}

// NotifyFlushDone retains the first disk failure and wakes every waiter.
func (b *AggregateWriteBuffer) NotifyFlushDone(errs ...error) {
	b.mu.Lock()
	if len(errs) > 0 && errs[0] != nil && b.err == nil {
		b.err = errs[0]
	}
	if b.isFlushing {
		b.completed = b.bufferSequence[1-b.activeIdx]
		b.isFlushing = false
	}
	var failed []PendingWriter
	if b.err != nil {
		failed = b.pendingWriters[b.activeIdx]
		b.pendingWriters[b.activeIdx] = nil
		b.bufferPos[b.activeIdx] = 0
	}
	err := b.err
	b.signalLocked()
	b.mu.Unlock()
	for _, w := range failed {
		w.OnLogFlush(err)
	}
}
func (b *AggregateWriteBuffer) Fail(err error) {
	if err == nil {
		return
	}
	b.mu.Lock()
	if b.err == nil {
		b.err = err
	}
	b.signalLocked()
	b.mu.Unlock()
}
func (b *AggregateWriteBuffer) stopAdds() {
	b.mu.Lock()
	b.closed = true
	b.signalLocked()
	b.mu.Unlock()
}
func (b *AggregateWriteBuffer) GetFlushChan() <-chan FlushTask { return b.flushChan }
func (b *AggregateWriteBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bufferPos = [2]int{}
	b.activeIdx = 0
	b.isFlushing = false
	b.writeCursor = 0
	b.sequence = 0
	b.completed = 0
	b.generation = 0
	b.err = nil
	b.closed = false
	b.signalLocked()
}
func (b *AggregateWriteBuffer) DirAggBufValid(offset int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, idx := range []int{b.activeIdx, 1 - b.activeIdx} {
		if idx != b.activeIdx && !b.isFlushing {
			continue
		}
		if offset >= b.bufferStart[idx] && offset < b.bufferStart[idx]+int64(b.bufferPos[idx]) {
			return true
		}
	}
	return false
}
func (b *AggregateWriteBuffer) CopyFrom(offset int64, p []byte) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, idx := range []int{b.activeIdx, 1 - b.activeIdx} {
		if idx != b.activeIdx && !b.isFlushing {
			continue
		}
		off := offset - b.bufferStart[idx]
		if off >= 0 && off < int64(b.bufferPos[idx]) {
			return copy(p, b.buffers[idx][off:b.bufferPos[idx]])
		}
	}
	return 0
}
