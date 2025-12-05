package bakemono

import (
	"crypto/md5"
	"sync"
)

// CacheWriter implements streaming write
type CacheWriter struct {
	stripe      *Stripe
	key         []byte // Original key
	firstKey    []byte
	earliestKey []byte
	currentKey  []byte

	buffer []byte // Buffer for current fragment

	totalSize     uint64
	fragmentCount uint32
	fragmentSize  uint32

	// RWW support
	mutex  sync.RWMutex
	closed bool
	od     *OpenDirEntry
}

func (s *Stripe) NewWriter(key []byte) (*CacheWriter, error) {
	if len(key) > MaxKeyLength {
		return nil, ErrChunkKeyTooLarge
	}

	w := &CacheWriter{
		stripe:       s,
		key:          key,
		buffer:       make([]byte, 0, ChunkDataSize),
		fragmentSize: uint32(ChunkDataSize),
	}

	// Initialize keys similar to ATS
	// first_key = key
	w.firstKey = make([]byte, len(key))
	copy(w.firstKey, key)

	// Generate earliest_key (random or hashed to avoid collision with first_key)
	// For simplicity, use MD5(key) as earliest_key base
	h := md5.Sum(key)
	w.earliestKey = h[:]
	w.currentKey = w.earliestKey

	// Register to OpenDir if available
	if s.OpenDir != nil {
		s.OpenDir.OpenWrite(key, w)
	}

	return w, nil
}

// Write implements io.Writer
func (w *CacheWriter) Write(p []byte) (n int, err error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	n = len(p)
	totalWritten := 0

	for totalWritten < n {
		spaceLeft := int(w.fragmentSize) - len(w.buffer)
		toWrite := n - totalWritten
		if toWrite > spaceLeft {
			toWrite = spaceLeft
		}

		w.buffer = append(w.buffer, p[totalWritten:totalWritten+toWrite]...)
		totalWritten += toWrite
		w.totalSize += uint64(toWrite)

		if len(w.buffer) == int(w.fragmentSize) {
			// Release lock temporarily to avoid blocking readers during flush?
			// Or keep lock to ensure consistency?
			// Flush involves I/O (or at least AggBuffer write), so it might be slow.
			// But writeChunkLocked acquires stripe lock.
			// If we hold w.mutex while calling flushFragment -> writeChunkLocked -> stripe.mutex
			// Reader calls openReadFromWriter -> acquires w.mutex.
			// Reader calls readChunkInternal -> acquires stripe.mutex.
			// Lock order: w.mutex -> stripe.mutex. Consistent.

			if err := w.flushFragmentLocked(false); err != nil {
				return totalWritten, err
			}
		}
	}

	return n, nil
}

// flushFragmentLocked must be called with w.mutex held
func (w *CacheWriter) flushFragmentLocked(isLast bool) error {
	if len(w.buffer) == 0 && !isLast {
		return nil
	}

	// Create Chunk
	ck := &Chunk{}
	// Make a copy of buffer because we might reset w.buffer
	// Actually Set copies data.
	if err := ck.Set(w.currentKey, w.buffer); err != nil {
		return err
	}

	// ATS-style: Data fragments use earliest_key sequence
	// Head fragment (written on Close) uses first_key

	// Write to Stripe
	w.stripe.mutex.Lock()
	off, err := w.stripe.writeChunkLocked(ck)
	if err != nil {
		w.stripe.mutex.Unlock()
		return err
	}

	// Update Directory
	binLenOnDisk := ck.GetBinaryLength()
	w.stripe.Dm.Set(w.currentKey, Offset(off), int(binLenOnDisk))
	w.stripe.mutex.Unlock()

	// Prepare for next fragment
	w.fragmentCount++

	// Prepare next key
	w.currentKey = NextCacheKey(w.currentKey)
	w.buffer = w.buffer[:0] // Reset buffer

	return nil
}

func (w *CacheWriter) flushFragment(isLast bool) error {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return w.flushFragmentLocked(isLast)
}

func (w *CacheWriter) Close() error {
	w.mutex.Lock()
	// 1. Flush remaining data (if any) as the last data fragment
	if len(w.buffer) > 0 {
		if err := w.flushFragmentLocked(true); err != nil {
			w.mutex.Unlock()
			return err
		}
	}
	w.closed = true
	w.mutex.Unlock()

	// 2. Write First Doc (Metadata)
	// Payload is Vector (containing Alternates / HTTPInfo)
	// We create a single HTTPInfo for now.

	info := HTTPInfo{
		PayloadSize:  w.totalSize,
		FragmentSize: w.fragmentSize,
	}
	copy(info.EarliestKey[:], w.earliestKey)
	// Headers are empty for now

	vector := Vector{
		Alternates: []HTTPInfo{info},
	}

	metaData, err := vector.MarshalBinary()
	if err != nil {
		return err
	}

	headCk := &Chunk{}
	if err := headCk.Set(w.firstKey, metaData); err != nil {
		return err
	}

	// Mark as head/first doc if we had specific flags in Doc, but we rely on key lookup now.
	// In ATS, First Doc contains the vector. Here it contains ObjectMetadata.

	w.stripe.mutex.Lock()
	off, err := w.stripe.writeChunkLocked(headCk)
	if err != nil {
		w.stripe.mutex.Unlock()
		return err
	}

	binLenOnDisk := headCk.GetBinaryLength()
	w.stripe.Dm.Set(w.firstKey, Offset(off), int(binLenOnDisk))
	w.stripe.mutex.Unlock()

	// Unregister from OpenDir
	if w.stripe.OpenDir != nil {
		w.stripe.OpenDir.CloseWrite(w.key, w)
	}

	logger.Debugf("Stripe %d CacheWriter Closed: key=%x totalSize=%d fragments=%d", w.stripe.ID, w.firstKey, w.totalSize, w.fragmentCount)
	return nil
}

// Set is now a wrapper around CacheWriter for backward compatibility
func (s *Stripe) Set(key, value []byte) error {
	err := s.checkSetRequest(key, value)
	if err != nil {
		return err
	}

	w, err := s.NewWriter(key)
	if err != nil {
		return err
	}

	if _, err := w.Write(value); err != nil {
		w.Close() // Try to cleanup
		return err
	}

	return w.Close()
}
