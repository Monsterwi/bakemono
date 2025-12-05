package bakemono

import (
	"os"
	"sync"
)

const (
	AggBufferSize    = 8 * 1024 * 1024 // 8MB (increased to fit Chunk + Doc header)
	AggHighWaterMark = AggBufferSize / 2
)

type flushRequest struct {
	buffer []byte     // Buffer to flush
	length int        // Length to flush
	offset int64      // Disk offset
	done   chan error // Optional, for sync flush
}

type AggregateWriteBuffer struct {
	// Double buffers
	bufA []byte
	bufB []byte

	// Current active buffer state
	mu                 sync.RWMutex
	currentBuf         []byte // Points to bufA or bufB
	currentPos         int
	currentStartOffset int64 // Absolute disk offset where currentBuf starts
	usingA             bool  // true if writing to bufA

	// Pending flush state (protected by mu)
	pendingBuf      []byte
	pendingLen      int
	pendingWritePos int64
	pendingActive   bool // True if pendingBuf is valid and flushing

	// Async flush control
	flushCh chan flushRequest
	fp      *os.File
}

func NewAggregateWriteBuffer(fp *os.File) *AggregateWriteBuffer {
	a := &AggregateWriteBuffer{
		bufA:    make([]byte, AggBufferSize),
		bufB:    make([]byte, AggBufferSize),
		usingA:  true,
		flushCh: make(chan flushRequest, 16), // Buffered channel
		fp:      fp,
	}
	a.currentBuf = a.bufA

	// Start background flush loop
	go a.flushLoop()

	return a
}

func (a *AggregateWriteBuffer) flushLoop() {
	for req := range a.flushCh {
		_, err := a.fp.WriteAt(req.buffer[:req.length], req.offset)
		if req.done != nil {
			req.done <- err
		}

		// Mark pending as done
		a.mu.Lock()
		if a.pendingActive && &a.pendingBuf[0] == &req.buffer[0] {
			a.pendingActive = false
		}
		a.mu.Unlock()
	}
}

// WriteAt appends data to the aggregation buffer.
func (a *AggregateWriteBuffer) WriteAt(p []byte, diskOffset int64) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// If buffer is empty, set the start offset
	if a.currentPos == 0 {
		a.currentStartOffset = diskOffset
	}

	totalWritten := 0
	for len(p) > 0 {
		available := AggBufferSize - a.currentPos
		if available == 0 {
			// Buffer full, switch
			if err := a.switchBufferLocked(diskOffset); err != nil {
				return totalWritten, err
			}
			// After switch, new buffer starts at current diskOffset
			a.currentStartOffset = diskOffset
			available = AggBufferSize
		}

		toWrite := available
		if len(p) < toWrite {
			toWrite = len(p)
		}

		copy(a.currentBuf[a.currentPos:], p[:toWrite])
		a.currentPos += toWrite
		p = p[toWrite:]
		totalWritten += toWrite

		// Advance diskOffset for the next iteration
		diskOffset += int64(toWrite)
	}
	return totalWritten, nil
}

func (a *AggregateWriteBuffer) switchBufferLocked(nextDiskOffset int64) error {
	for a.pendingActive {
		a.mu.Unlock()
		// Yield/Busy wait
		a.mu.Lock()
	}

	flushOffset := nextDiskOffset - int64(a.currentPos)
	bufToFlush := a.currentBuf

	req := flushRequest{
		buffer: bufToFlush,
		length: a.currentPos,
		offset: flushOffset,
	}

	// Update pending state
	a.pendingBuf = bufToFlush
	a.pendingLen = a.currentPos
	a.pendingWritePos = flushOffset
	a.pendingActive = true

	// Switch
	if a.usingA {
		a.currentBuf = a.bufB
		a.usingA = false
	} else {
		a.currentBuf = a.bufA
		a.usingA = true
	}
	a.currentPos = 0

	// Send flush request
	a.flushCh <- req
	return nil
}

// FlushSync forces a flush of the current buffer.
func (a *AggregateWriteBuffer) FlushSync(diskOffset int64) error {
	a.mu.Lock()

	for a.pendingActive {
		a.mu.Unlock()
		a.mu.Lock()
	}

	if a.currentPos == 0 {
		a.mu.Unlock()
		return nil
	}

	startOffset := diskOffset - int64(a.currentPos)
	req := flushRequest{
		buffer: a.currentBuf,
		length: a.currentPos,
		offset: startOffset,
		done:   make(chan error, 1),
	}

	a.currentPos = 0
	a.mu.Unlock()

	a.flushCh <- req
	return <-req.done
}

// CopyFrom tries to read from memory buffers (Read-While-Write).
func (a *AggregateWriteBuffer) CopyFrom(p []byte, offset int64) (int, bool) {
	// Overlay logic implementation reused for CopyFrom
	return a.ReadOverlay(p, offset)
}

// ReadOverlay copies available data from memory buffers into p,
// overlaying any data that exists in the buffers.
// It handles partial overlaps (e.g. chunk starts on disk but ends in buffer).
func (a *AggregateWriteBuffer) ReadOverlay(p []byte, offset int64) (int, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	totalOverlaid := 0
	hit := false
	targetEnd := offset + int64(len(p))

	// Helper to intersect and copy
	overlay := func(buf []byte, bufStart int64, bufLen int, name string) {
		bufEnd := bufStart + int64(bufLen)

		// Intersection of [offset, targetEnd) and [bufStart, bufEnd)
		start := offset
		if bufStart > start {
			start = bufStart
		}
		end := targetEnd
		if bufEnd < end {
			end = bufEnd
		}

		if start < end {
			// Valid intersection
			len := int(end - start)
			destOffset := int(start - offset)
			srcOffset := int(start - bufStart)
			copy(p[destOffset:], buf[srcOffset:srcOffset+len])
			totalOverlaid += len
			hit = true
			logger.Debugf("Overlay from %s: offset=%d len=%d", name, start, len)
		}
	}

	// 1. Check Pending Buffer
	if a.pendingActive && a.pendingBuf != nil {
		overlay(a.pendingBuf, a.pendingWritePos, a.pendingLen, "Pending")
	}

	// 2. Check Current Buffer
	overlay(a.currentBuf, a.currentStartOffset, a.currentPos, "Current")

	return totalOverlaid, hit
}

// IsFullyInCache checks if the requested range [offset, offset+size)
// is completely contained within the current or pending aggregation buffers.
// This is used for fast-path reads (LmemHit).
func (a *AggregateWriteBuffer) IsFullyInCache(offset int64, size int64) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()

	end := offset + size

	// 1. Check Pending Buffer
	if a.pendingActive && a.pendingBuf != nil {
		pStart := a.pendingWritePos
		pEnd := pStart + int64(a.pendingLen)
		// Check if range is fully inside pending buffer
		if offset >= pStart && end <= pEnd {
			return true
		}
	}

	// 2. Check Current Buffer
	cStart := a.currentStartOffset
	cEnd := cStart + int64(a.currentPos)
	// Check if range is fully inside current buffer
	if offset >= cStart && end <= cEnd {
		return true
	}

	return false
}

func (a *AggregateWriteBuffer) BufferPos() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.currentPos
}

func (a *AggregateWriteBuffer) Close() error {
	close(a.flushCh)
	return nil
}

func (a *AggregateWriteBuffer) Empty() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.currentPos == 0
}

func (a *AggregateWriteBuffer) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.currentPos = 0
	a.pendingActive = false
}
