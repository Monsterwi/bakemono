package cache

import (
	"sync"
)

const (
	AggBufferSize    = 4 * 1024 * 1024 // 4MB
	AggHighWaterMark = AggBufferSize / 2
)

// PendingWriter represents a writer waiting for the buffer to be flushed.
// In ATS this is CacheVC.
type PendingWriter interface {
	OnLogFlush(err error)
}

// FlushTask represents a buffer that needs to be flushed
type FlushTask struct {
	Buffer  []byte
	Writers []PendingWriter
	// The offset where this buffer starts (relative to the stream or absolute?)
	// Not strictly needed if we just append, but good for debug
}

type AggregateWriteBuffer struct {
	// Double buffering
	buffers   [2][]byte
	bufferPos [2]int
	activeIdx int

	// Current write cursor (absolute disk offset)
	writeCursor int64

	// Pending writers for the active buffer
	pendingWriters [2][]PendingWriter

	// Synchronization
	mu sync.Mutex
	// Condition to wait when both buffers are full
	cond *sync.Cond

	// Flush coordination
	flushChan chan FlushTask
	// Track if the other buffer is currently flushing/busy
	isFlushing bool

	// Stripe Bounds
	minOffset int64
	maxOffset int64
}

func NewAggregateWriteBuffer() *AggregateWriteBuffer {
	b := &AggregateWriteBuffer{
		buffers: [2][]byte{
			make([]byte, AggBufferSize),
			make([]byte, AggBufferSize),
		},
		pendingWriters: [2][]PendingWriter{
			make([]PendingWriter, 0),
			make([]PendingWriter, 0),
		},
		activeIdx: 0,
		flushChan: make(chan FlushTask, 1), // Buffer of 1 (the other buffer)
	}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *AggregateWriteBuffer) Init(startOffset int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writeCursor = startOffset
	// TODO: remove this
	b.minOffset = startOffset
	// Default maxOffset to very large if not set (or 0 implies no limit?)
	// To avoid breaking existing tests that don't set bounds, we can leave it 0 and check if > 0
}

// TODO: remove this
func (b *AggregateWriteBuffer) SetBounds(min, max int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.minOffset = min
	b.maxOffset = max
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

// GetWriteCursor returns the current absolute write position for the next byte
func (b *AggregateWriteBuffer) GetWriteCursor() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writeCursor
}

// Add adds a document to the buffer.
// Returns the absolute disk offset where the data will be written.
func (b *AggregateWriteBuffer) Add(data []byte, approxSize int, writer PendingWriter) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Wait if current buffer is full AND we can't swap (other buffer busy)
	// Or if current buffer space is insufficient
	for {
		active := b.buffers[b.activeIdx]
		pos := b.bufferPos[b.activeIdx]

		// TODO: remove this
		// Check Stripe Wrapping
		// If adding this data would cross maxOffset, we MUST flush and wrap.
		// Note: This assumes one chunk fits in maxOffset-minOffset (stripe size).
		if b.maxOffset > 0 && b.writeCursor+int64(len(data)) > b.maxOffset {
			if !b.isFlushing {
				b.swapAndFlushLocked()
				// Reset cursor to minOffset
				b.writeCursor = b.minOffset
				// Continue loop to add data to new buffer (at minOffset)
				continue
			}
			// Must wait for flush to complete before wrapping
			b.cond.Wait()
			continue
		}

		if len(data) <= len(active)-pos {
			// Fits in current buffer
			break
		}

		// Current buffer full/insufficient.
		// Try to swap.
		if !b.isFlushing {
			b.swapAndFlushLocked()
			// After swap, re-check space (the new buffer is empty)
			continue
		}

		// Other buffer is busy, we must wait (Backpressure)
		b.cond.Wait()
	}

	// Write to active buffer
	idx := b.activeIdx
	pos := b.bufferPos[idx]
	copy(b.buffers[idx][pos:], data)
	b.bufferPos[idx] += len(data)

	if writer != nil {
		b.pendingWriters[idx] = append(b.pendingWriters[idx], writer)
	}

	// Capture return offset
	currentOffset := b.writeCursor
	b.writeCursor += int64(len(data))

	// Check High Water Mark
	if b.bufferPos[idx] >= AggHighWaterMark && !b.isFlushing {
		b.swapAndFlushLocked()
	}

	return currentOffset, nil
}

// swapAndFlushLocked initiates a flush of the current active buffer.
// Must be called with lock held.
func (b *AggregateWriteBuffer) swapAndFlushLocked() {
	idx := b.activeIdx
	if b.bufferPos[idx] == 0 {
		return
	}

	// Prepare task
	task := FlushTask{
		Buffer:  b.buffers[idx][:b.bufferPos[idx]], // Slice it
		Writers: b.pendingWriters[idx],
	}

	// Swap
	b.isFlushing = true
	b.activeIdx = (idx + 1) % 2
	b.bufferPos[b.activeIdx] = 0 // Reset new active
	b.pendingWriters[b.activeIdx] = b.pendingWriters[b.activeIdx][:0]

	// Send to flusher
	select {
	case b.flushChan <- task:
		// Sent
	default:
		// Should not happen if isFlushing logic is correct (channel size 1)
		panic("flush channel full")
	}
}

// TriggerFlush initiates a flush asynchronously if there is data.
func (b *AggregateWriteBuffer) TriggerFlush() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.isFlushing {
		b.swapAndFlushLocked()
	}
}

// FlushSync forces a flush and waits for it to complete.
func (b *AggregateWriteBuffer) FlushSync() error {
	b.mu.Lock()
	// Trigger flush of current data
	b.swapAndFlushLocked()

	for b.isFlushing {
		b.cond.Wait()
	}
	b.mu.Unlock()
	return nil
}

// NotifyFlushDone is called by the background worker when flush is complete
func (b *AggregateWriteBuffer) NotifyFlushDone() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.isFlushing = false
	b.cond.Signal()    // Wake up writers waiting for space
	b.cond.Broadcast() // Or broadcast to wake FlushSync
}

// GetFlushChan returns the channel to receive flush tasks
func (b *AggregateWriteBuffer) GetFlushChan() <-chan FlushTask {
	return b.flushChan
}

// Reset resets the buffer (used in tests usually)
func (b *AggregateWriteBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bufferPos[0] = 0
	b.bufferPos[1] = 0
	b.activeIdx = 0
	b.isFlushing = false
	b.writeCursor = 0
}

// DirAggBufValid checks if the directory entry is within the aggregation buffer.
// Aligned with ATS Stripe::dir_agg_buf_valid.
func (b *AggregateWriteBuffer) DirAggBufValid(dirOffset int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Check Active Buffer
	startActive := b.writeCursor - int64(b.bufferPos[b.activeIdx])
	if dirOffset >= startActive && dirOffset < b.writeCursor {
		return true
	}

	// Check Flushing Buffer (if any)
	if b.isFlushing {
		flushIdx := (b.activeIdx + 1) % 2
		lenFlush := b.bufferPos[flushIdx]
		startFlush := startActive - int64(lenFlush)

		if dirOffset >= startFlush && dirOffset < startActive {
			return true
		}
	}

	return false
}

// CopyFrom attempts to copy data from the buffer
// Returns n bytes read, or 0 if range is not in buffer.
func (b *AggregateWriteBuffer) CopyFrom(diskOffset int64, p []byte) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Check Active Buffer
	startActive := b.writeCursor - int64(b.bufferPos[b.activeIdx])
	if diskOffset >= startActive {
		off := int(diskOffset - startActive)
		if off < b.bufferPos[b.activeIdx] {
			return copy(p, b.buffers[b.activeIdx][off:b.bufferPos[b.activeIdx]])
		}
	}

	// Check Flushing Buffer (if any)
	if b.isFlushing {
		flushIdx := (b.activeIdx + 1) % 2
		lenFlush := b.bufferPos[flushIdx]
		startFlush := startActive - int64(lenFlush)

		if diskOffset >= startFlush && diskOffset < startActive {
			off := int(diskOffset - startFlush)
			return copy(p, b.buffers[flushIdx][off:lenFlush])
		}
	}

	return 0
}
