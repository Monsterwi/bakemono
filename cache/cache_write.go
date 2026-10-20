package cache

import (
	"fmt"
	"io"
	"sync"
)

// CacheWriter handles writing data to the cache.
// It buffers data and flushes to the AggregateWriteBuffer.
type CacheWriter struct {
	Stripe *Stripe
	Key    []byte

	// Metadata
	Info *CacheHTTPInfo

	// Write buffer
	buffer []byte

	// Streaming State
	frag0Data    []byte // Holds data for Fragment 0 (waiting for TotalLen)
	nextFragIdx  int    // Index of the next fragment to write
	totalWritten int64  // Total bytes written by caller

	// Streaming synchronization
	cond *sync.Cond

	// State
	mutex  sync.RWMutex
	closed bool
	err    error         // Error state if writer failed
	od     *OpenDirEntry // Reference to OpenDir entry
}

func NewCacheWriter(s *Stripe, key []byte) *CacheWriter {
	w := &CacheWriter{
		Stripe: s,
		Key:    key,
		buffer: make([]byte, 0, 4096), // Initial buffer size
	}
	w.cond = sync.NewCond(&w.mutex)
	return w
}

// ReadAt implements a simpler interface for readers to get data from the writer.
// It abstracts away the fragment logic from the consumer.
// It returns bytes read, and an error if read failed or EOF.
// Uses read lock for concurrent readers, upgrades to write lock only when waiting.
func (w *CacheWriter) ReadAt(offset int64, p []byte) (n int, err error) {
	// If request size is 0, return
	if len(p) == 0 {
		return 0, nil
	}

	for {
		// Use read lock for reading data
		w.mutex.RLock()

		// Calculate available data based on what's been written
		// w.totalWritten includes buffered data + flushed fragments
		available := w.totalWritten - offset

		if available > 0 {
			// We have data! But where is it?
			// It could be in flushed fragments (RAM/Disk) or in current buffer.

			// Current buffer starts at: w.totalWritten - len(w.buffer)
			bufferStart := w.totalWritten - int64(len(w.buffer))

			if offset >= bufferStart {
				// It's in the buffer!
				bufOffset := offset - bufferStart
				toRead := int64(len(p))
				if toRead > available {
					toRead = available
				}
				copy(p, w.buffer[bufOffset:bufOffset+toRead])
				w.mutex.RUnlock()
				return int(toRead), nil
			}

			// Check frag0Data
			// frag0Data contains the first fragment (offset 0 to len(frag0Data))
			// It is kept in memory until Close().
			if len(w.frag0Data) > 0 {
				if offset < int64(len(w.frag0Data)) {
					// It's in frag0Data!
					toRead := int64(len(p))
					frag0Avail := int64(len(w.frag0Data)) - offset
					if toRead > frag0Avail {
						toRead = frag0Avail
					}
					copy(p, w.frag0Data[offset:offset+toRead])
					w.mutex.RUnlock()
					return int(toRead), nil
				}
			}

			// If we are here, the data is OLDER than current buffer AND not in frag0Data (or offset > frag0Data).
			// It implies it has been flushed to disk/RAM.
			w.mutex.RUnlock()
			return 0, ErrReadFromDisk // Sentinel error
		}

		// No data available yet
		if w.closed {
			err := w.err
			w.mutex.RUnlock()
			if err != nil {
				return 0, err
			}
			return 0, io.EOF
		}

		// Need to wait for more data
		// cond.Wait() requires write lock, so we need to upgrade
		w.mutex.RUnlock()

		// Upgrade to write lock for cond.Wait()
		w.mutex.Lock()
		// Double-check after acquiring write lock
		if w.closed {
			err := w.err
			w.mutex.Unlock()
			if err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		// Wait for more data (releases lock, re-acquires on return)
		w.cond.Wait()
		w.mutex.Unlock()
		// Loop back to try reading again with read lock
	}
}

// Abort signals that the write operation failed.
func (w *CacheWriter) Abort(err error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.closed {
		return
	}

	w.err = err
	w.closed = true
	w.cond.Broadcast()
}

// REFACTOR: direct call to AggBuffer.Add
// Write appends data to the writer.
func (w *CacheWriter) Write(p []byte) (n int, err error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.closed {
		return 0, fmt.Errorf("writer closed")
	}

	// Append new data to buffer
	w.buffer = append(w.buffer, p...)
	w.totalWritten += int64(len(p))

	// Notify waiting readers that new data is available
	w.cond.Broadcast()

	// Try to flush chunks
	if err := w.processBuffer(); err != nil {
		// If flush fails, we should Abort self?
		// Or just return error.
		// Let's set error state so readers know.
		w.err = err
		w.closed = true
		w.cond.Broadcast()
		return 0, err
	}

	return len(p), nil
}

// processBuffer checks if we have enough data to write fragments.
func (w *CacheWriter) processBuffer() error {
	chunkSize := TargetFragmentSize

	for len(w.buffer) >= chunkSize {
		// Extract a chunk
		chunk := w.buffer[:chunkSize]

		if w.nextFragIdx == 0 {
			// Special case for Fragment 0:
			// We CANNOT write it yet because we don't know TotalLen (unless we want to update it later, which is hard).
			// We hold it in memory.
			w.frag0Data = make([]byte, len(chunk))
			copy(w.frag0Data, chunk)

			// Advance buffer
			w.buffer = w.buffer[chunkSize:]
			w.nextFragIdx++
		} else {
			// Fragment 1..N: Write immediately
			// We don't need CacheHTTPInfo for these fragments (hlen=0)
			err := w.writeFragment(w.nextFragIdx, chunk, nil)
			if err != nil {
				return err
			}

			// Advance buffer
			w.buffer = w.buffer[chunkSize:]
			w.nextFragIdx++
		}
	}
	return nil
}

// Close finishes the write operation.
// It writes remaining data and finally Fragment 0.
func (w *CacheWriter) Close() error {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.closed {
		return nil
	}

	// Serialize Metadata (CacheHTTPInfo) for Fragment 0
	var infoBytes []byte
	var err error
	if w.Info != nil {
		infoBytes, err = w.Info.MarshalBinary()
		if err != nil {
			return fmt.Errorf("failed to marshal http info: %v", err)
		}
	}

	// Handle remaining buffer
	if len(w.buffer) > 0 {
		if w.nextFragIdx == 0 {
			// Total data < chunkSize. Frag 0 is everything.
			// We haven't set frag0Data yet.
			w.frag0Data = w.buffer
			w.buffer = nil
			// We will write it below as Frag 0.
		} else {
			// We have a partial last fragment (Frag N)
			err := w.writeFragment(w.nextFragIdx, w.buffer, nil)
			if err != nil {
				return err
			}
			w.nextFragIdx++
			w.buffer = nil
		}
	}

	// Now write Fragment 0 (if we have data OR if it's an empty object)
	// Even empty object needs Frag 0 with Metadata.
	if w.frag0Data == nil && w.totalWritten == 0 {
		w.frag0Data = []byte{}
	}

	if w.frag0Data != nil {
		// Write Fragment 0
		// This is the commit point.
		err := w.writeFragment(0, w.frag0Data, infoBytes)
		if err != nil {
			return err
		}
	}

	w.closed = true
	// Notify any remaining readers that we are done (EOF)
	w.cond.Broadcast()

	// Remove from OpenDir logic is handled by CacheVC / OpenDir wrapper
	// We don't call OpenDir.CloseWrite here directly anymore to keep layering clean,
	// or we rely on the OpenDirEntry's refcount.
	// But strictly speaking, writer finishing means it's no longer the "Active Writer".
	// The entry might remain for readers.

	// If the OpenDirEntry is linked, we should probably mark it?
	// But in our new design, the Writer struct persists in the OpenDirEntry.

	return nil
}

// writeFragment writes a single fragment to storage.
func (w *CacheWriter) writeFragment(idx int, data []byte, infoBytes []byte) error {
	// Determine keys
	thisKey := FragmentKey(w.Key, idx)

	// Padded keys for Doc struct
	var firstKey [16]byte
	copy(firstKey[:], w.Key)
	var currentKey [16]byte
	copy(currentKey[:], thisKey)

	// Info is only for Fragment 0
	currentHLen := uint32(len(infoBytes))

	// Calculate size
	docPayloadSize := len(infoBytes) + len(data)
	totalDocSize := DocHeaderSize + docPayloadSize
	approxSize := (totalDocSize + SectorSize - 1) / SectorSize * SectorSize

	doc := &Doc{
		Magic:    DocMagic,
		Len:      uint32(totalDocSize),
		TotalLen: uint64(w.totalWritten), // Final total len (valid for Frag 0 at Close)
		FirstKey: firstKey,
		Key:      currentKey,
		HLen:     currentHLen,
	}

	// Serialize Doc Struct
	docBytes, err := doc.MarshalBinary()
	if err != nil {
		return err
	}

	// Combine: [DocStruct] + [HTTPInfo] + [Data]
	fullData := make([]byte, 0, totalDocSize)
	fullData = append(fullData, docBytes...)
	if len(infoBytes) > 0 {
		fullData = append(fullData, infoBytes...)
	}
	fullData = append(fullData, data...)

	// Checksum
	doc.Checksum = doc.CalculateChecksum(fullData[DocHeaderSize:])

	// Patch Checksum
	docBytes, _ = doc.MarshalBinary()
	copy(fullData[0:], docBytes)

	// Add to AggBuffer
	// New API: Add returns the absolute disk offset where this will be written
	diskOffset, err := w.Stripe.AggBuffer.Add(fullData, approxSize, nil)
	if err != nil {
		return err
	}

	// Populate RamCache (Fragment)
	if w.Stripe.RamCache != nil {
		w.Stripe.RamCache.Put(thisKey, fullData)
	}

	// Update Directory (Fragment)
	if w.Stripe.DirMgr != nil {
		// Get current phase from Stripe
		w.Stripe.mu.RLock()
		phase := w.Stripe.Phase
		w.Stripe.mu.RUnlock()

		_, err := w.Stripe.DirMgr.Set(thisKey, Offset(diskOffset), approxSize, phase)
		if err != nil {
			return fmt.Errorf("failed to update dir for fragment %d: %v", idx, err)
		}
		// update dir trie for prefix purge (only for fragment 0, which uses the original key)
		if idx == 0 && w.Stripe.DirMgr.DirTrie != nil {
			w.Stripe.DirMgr.DirTrie.Set(w.Key)
		}
	}

	return nil
}
