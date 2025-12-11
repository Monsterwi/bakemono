package cache

import (
	"fmt"
	"io"
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

type AggregateWriteBuffer struct {
	buffer                  []byte
	bufferPos               int
	bytesPendingAggregation int
	pendingWriters          []PendingWriter
	mu                      sync.Mutex
}

func NewAggregateWriteBuffer() *AggregateWriteBuffer {
	return &AggregateWriteBuffer{
		buffer:         make([]byte, AggBufferSize),
		bufferPos:      0,
		pendingWriters: make([]PendingWriter, 0),
	}
}

func (b *AggregateWriteBuffer) IsEmpty() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bufferPos == 0
}

func (b *AggregateWriteBuffer) GetBufferPos() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bufferPos
}

func (b *AggregateWriteBuffer) GetBytesPendingAggregation() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bytesPendingAggregation
}

func (b *AggregateWriteBuffer) AddBytesPendingAggregation(size int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bytesPendingAggregation += size
}

// Add adds a document to the buffer.
// data includes the Doc struct serialized, header, and body.
// approxSize is the size occupied in the directory/volume (rounded to sectors).
func (b *AggregateWriteBuffer) Add(data []byte, approxSize int, writer PendingWriter) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(data) > len(b.buffer)-b.bufferPos {
		return fmt.Errorf("buffer overflow")
	}

	copy(b.buffer[b.bufferPos:], data)
	b.bufferPos += len(data)
	b.bytesPendingAggregation -= approxSize // In ATS this subtracts because it's moving from "pending" to "buffered" (which is also pending flush, but distinct state?)
	// Actually ATS: `approx_size` will be subtracted from the bytes pending aggregation.
	// This implies `bytesPendingAggregation` tracks bytes *before* they are put into the buffer?
	// Let's assume the caller manages `bytesPendingAggregation` logic or we just follow ATS.

	if writer != nil {
		b.pendingWriters = append(b.pendingWriters, writer)
	}

	return nil
}

// Flush writes the buffer to the provided writer (usually a file/disk).
// After flush, it notifies pending writers and resets the buffer.
func (b *AggregateWriteBuffer) Flush(w io.WriterAt, writePos int64) error {
	b.mu.Lock()
	// Copy buffer content to avoid holding lock during IO?
	// ATS calls flush with file descriptor.
	// Here we use WriterAt.
	toWrite := b.buffer[:b.bufferPos]
	writers := make([]PendingWriter, len(b.pendingWriters))
	copy(writers, b.pendingWriters)

	// Reset buffer immediately?
	// ATS: "Flushing the buffer only writes the buffer to disk; it does not modify the contents of the buffer. To reset the buffer, call reset_buffer_pos()."
	// But usually flush is followed by reset.
	// We will just write.
	b.mu.Unlock()

	_, err := w.WriteAt(toWrite, writePos)

	// Notify writers
	for _, writer := range writers {
		if writer != nil {
			writer.OnLogFlush(err)
		}
	}

	return err
}

func (b *AggregateWriteBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.bufferPos = 0
	b.pendingWriters = b.pendingWriters[:0]
}

// CopyFrom copies data from the buffer.
// This is used to serve reads from the write buffer (Read-While-Write).
func (b *AggregateWriteBuffer) CopyFrom(dest []byte, offset int) int {
	b.mu.Lock()
	defer b.mu.Unlock()

	if offset >= b.bufferPos {
		return 0
	}

	n := copy(dest, b.buffer[offset:b.bufferPos])
	return n
}
