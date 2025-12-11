package cache

import (
	"fmt"
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
	// offset int // unused

	// Streaming State
	frag0Data    []byte // Holds data for Fragment 0 (waiting for TotalLen)
	nextFragIdx  int    // Index of the next fragment to write
	totalWritten int64  // Total bytes written by caller

	// State
	mutex  sync.RWMutex
	closed bool
	od     *OpenDirEntry // Reference to OpenDir entry
}

func NewCacheWriter(s *Stripe, key []byte) *CacheWriter {
	return &CacheWriter{
		Stripe: s,
		Key:    key,
		buffer: make([]byte, 0, 4096), // Initial buffer size
		Info:   NewCacheHTTPInfo(),    // Initialize with empty info
	}
}

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

	// Try to flush chunks
	if err := w.processBuffer(); err != nil {
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

	// Remove from OpenDir
	if w.Stripe.OpenDir != nil {
		w.Stripe.OpenDir.CloseWrite(w.Key, w)
	}

	w.closed = true
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
	currentAggPos := w.Stripe.AggBuffer.GetBufferPos()
	diskOffset := w.Stripe.WritePos + int64(currentAggPos)

	err = w.Stripe.AggBuffer.Add(fullData, approxSize, nil)
	if err != nil {
		return err
	}

	// Populate RamCache (Fragment)
	if w.Stripe.RamCache != nil {
		w.Stripe.RamCache.Put(thisKey, fullData)
	}

	// Update Directory (Fragment)
	if w.Stripe.DirMgr != nil {
		_, err := w.Stripe.DirMgr.Set(thisKey, Offset(diskOffset), approxSize)
		if err != nil {
			return fmt.Errorf("failed to update dir for fragment %d: %v", idx, err)
		}
	}

	return nil
}
