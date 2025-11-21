package bakemono

import (
	"os"
)

const (
	AggHighWaterMark = ChunkDataSize
	AggBufferSize    = AggHighWaterMark * 2
)

type AggregateWriteBuffer struct {
	buffer    []byte
	bufferPos int
	fp        *os.File
}

func NewAggregateWriteBuffer(fp *os.File) *AggregateWriteBuffer {
	return &AggregateWriteBuffer{
		buffer: make([]byte, AggBufferSize),
		fp:     fp,
	}
}

func (a *AggregateWriteBuffer) WriteAt(p []byte, off int64) (n int, err error) {
	n = copy(a.buffer[off:], p)
	a.bufferPos += n
	return n, nil
}

func (a *AggregateWriteBuffer) ReadAt(p []byte, off int64) (n int, err error) {
	right := min(off+int64(len(p)), AggBufferSize)
	return copy(p, a.buffer[off:right]), nil
}

func (a *AggregateWriteBuffer) Close() error { return nil }

func (a *AggregateWriteBuffer) Flush(off int64) (n int, err error) {
	return a.fp.WriteAt(a.buffer[:a.bufferPos], off)
}

func (a *AggregateWriteBuffer) Empty() bool {
	return a.bufferPos == 0
}

func (a *AggregateWriteBuffer) Reset() {
	a.bufferPos = 0
}
