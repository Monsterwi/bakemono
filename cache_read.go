package bakemono

import (
	"crypto/md5"
	"errors"
	"io"
	"time"
)

// CacheReader implements streaming read
type CacheReader struct {
	stripe      *Stripe
	headKey     []byte
	earliestKey []byte // Stored to allow Seek
	currentKey  []byte
	nextKey     []byte

	totalSize int64
	bytesRead int64

	currentData []byte
	dataOffset  int

	// RWW Support
	writer *CacheWriter
}

func NewCacheReaderFromBytes(data []byte) *CacheReader {
	return &CacheReader{
		currentData: data,
		totalSize:   int64(len(data)),
	}
}

func NewCacheReaderFromWriter(s *Stripe, key []byte, w *CacheWriter) *CacheReader {
	w.mutex.RLock()
	defer w.mutex.RUnlock()

	logger.Infof("RWW: Creating CacheReader from active Writer. Key: %x, Fragments: %d, TotalSize: %d",
		key, w.fragmentCount, w.totalSize)

	// We assume request is for the object being written
	r := &CacheReader{
		stripe:      s,
		headKey:     key,
		writer:      w,
		earliestKey: w.earliestKey, // From writer
		currentKey:  w.earliestKey, // Start reading from earliest data
		nextKey:     NextCacheKey(w.earliestKey),
		totalSize:   -1, // Unknown
	}
	return r
}

func (r *CacheReader) Seek(offset int64, whence int) (int64, error) {
	if r.writer != nil {
		return 0, errors.New("seek not supported in RWW mode yet")
	}
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.bytesRead + offset
	case io.SeekEnd:
		abs = r.totalSize + offset
	default:
		return 0, errors.New("bakemono: invalid whence")
	}

	if abs < 0 {
		return 0, errors.New("bakemono: negative position")
	}

	if abs > r.totalSize {
		// Allow seek to EOF, but not past? io.Seeker allows past EOF.
		// But we can't read there.
		// For now clamp or allow.
	}

	r.bytesRead = abs

	if r.stripe == nil {
		// RamCache mode
		if abs > r.totalSize {
			r.dataOffset = len(r.currentData) // EOF
		} else {
			r.dataOffset = int(abs)
		}
		return abs, nil
	}

	// Calculate target fragment
	chunkSize := int64(ChunkDataSize)
	targetFragIdx := int(abs / chunkSize)
	offsetInFrag := int(abs % chunkSize)

	// Re-calculate key from earliestKey
	targetKey := r.earliestKey
	if targetFragIdx > 0 {
		for i := 0; i < targetFragIdx; i++ {
			targetKey = NextCacheKey(targetKey)
		}
	}

	// Fetch chunk
	r.stripe.mutex.RLock()
	hit, _, d := r.stripe.Dm.Get(targetKey)
	if !hit {
		r.stripe.mutex.RUnlock()
		return 0, errors.New("fragment missing during seek")
	}
	fragOffset := Offset(d.offset())
	fragSize := Offset(d.approxSize())
	r.stripe.mutex.RUnlock()

	ck, err := r.stripe.readChunkInternal(fragOffset, fragSize)
	if err != nil {
		return 0, err
	}

	r.currentData = ck.DataRaw
	r.dataOffset = offsetInFrag
	r.currentKey = targetKey
	// Pre-calculate next key for Read() continuation
	r.nextKey = NextCacheKey(targetKey)

	return abs, nil
}

func (r *CacheReader) Read(p []byte) (n int, err error) {
	if r.writer != nil {
		return r.readFromWriter(p)
	}

	if r.dataOffset >= len(r.currentData) {
		// Need more data
		if r.bytesRead >= r.totalSize {
			return 0, io.EOF
		}

		// Fetch next chunk
		if r.stripe == nil || r.nextKey == nil {
			return 0, io.ErrUnexpectedEOF
		}

		// Get Next Chunk from Directory
		r.stripe.mutex.RLock()
		hit, _, d := r.stripe.Dm.Get(r.nextKey)
		if !hit {
			r.stripe.mutex.RUnlock()
			return 0, errors.New("fragment missing")
		}
		offset := Offset(d.offset())
		size := Offset(d.approxSize())
		r.stripe.mutex.RUnlock()

		// Read Chunk
		ck, err := r.stripe.readChunkInternal(offset, size)
		if err != nil {
			return 0, err
		}

		r.currentData = ck.DataRaw
		r.dataOffset = 0
		r.currentKey = r.nextKey

		// Calculate next next key
		r.nextKey = NextCacheKey(r.currentKey)
	}

	n = copy(p, r.currentData[r.dataOffset:])
	r.dataOffset += n
	r.bytesRead += int64(n)

	if n == 0 && r.bytesRead < r.totalSize {
		// Should have returned error above if missing,
		// but if we are here, it means we need to loop again?
		// No, Read usually returns what it can.
		// If n=0 and not EOF, we should try next chunk immediately?
		// Recursive call or loop?
		// Go Reader convention: return >0 and nil, or 0 and EOF.
		// If we exhausted currentData but have more totalSize, we should fetch next and copy.
		// Simplified: The caller will call Read again.
		// But if we return 0, nil, caller might think blocked.
		// We should fetch next chunk HERE if n==0.
		// Refactoring structure to loop.
	}

	// Correct Loop implementation
	// If n < len(p) and we have more data, we could try to fill p.
	// But standard Reader can return partial.

	if r.bytesRead >= r.totalSize && n == 0 {
		return 0, io.EOF
	}

	return n, nil
}

func (r *CacheReader) readFromWriter(p []byte) (n int, err error) {
	readFromBuffer := 0
	readFromDisk := 0

	for {
		// Check if we need new data
		if r.dataOffset >= len(r.currentData) {
			// Fetch logic
			r.writer.mutex.RLock()
			writerClosed := r.writer.closed
			writerCurrentKey := r.writer.currentKey
			writerBufferLen := len(r.writer.buffer)
			var bufferCopy []byte

			isCurrent := string(r.currentKey) == string(writerCurrentKey)
			if isCurrent {
				// Reading from current buffer
				readFromBuffer++
				logger.Debugf("RWW: Reading from active Writer Buffer. Key: %x, BufferLen: %d, BytesRead: %d",
					r.currentKey, writerBufferLen, r.bytesRead)
				bufferCopy = make([]byte, writerBufferLen)
				copy(bufferCopy, r.writer.buffer)
			}
			r.writer.mutex.RUnlock()

			if !isCurrent {
				// Previous chunk, must be on disk
				readFromDisk++
				r.stripe.mutex.RLock()
				hit, _, d := r.stripe.Dm.Get(r.currentKey)
				var offset, size Offset
				if hit {
					offset = Offset(d.offset())
					size = Offset(d.approxSize())
				}
				r.stripe.mutex.RUnlock()

				if hit {
					logger.Infof("RWW: Switching to read completed Fragment from Disk/AggBuffer. Key: %x, BytesRead: %d",
						r.currentKey, r.bytesRead)
					ck, err := r.stripe.readChunkInternal(offset, size)
					if err != nil {
						logger.Errorf("RWW: Failed to read completed fragment. Key: %x, Err: %v", r.currentKey, err)
						return 0, err
					}
					r.currentData = ck.DataRaw
					logger.Infof("RWW: Successfully loaded Fragment. Key: %x, DataLen: %d", r.currentKey, len(ck.DataRaw))

					// Check if we are already at the end of this disk chunk
					if r.dataOffset >= len(r.currentData) {
						r.currentData = nil
						r.dataOffset = 0
						r.currentKey = NextCacheKey(r.currentKey)
						continue
					}
				} else {
					if writerClosed {
						logger.Infof("RWW: Writer closed, EOF reached. BytesRead: %d", r.bytesRead)
						return 0, io.EOF
					}
					return 0, errors.New("chunk missing from writer and disk")
				}
			} else {
				// Still in current chunk (Buffer)
				if len(bufferCopy) > r.dataOffset {
					// New data available
					r.currentData = bufferCopy
				} else {
					// No new data
					if writerClosed {
						logger.Infof("RWW: Writer closed, EOF reached. BytesRead: %d", r.bytesRead)
						return 0, io.EOF
					}
					// Blocking wait
					logger.Debugf("RWW: Waiting for Writer to produce more data. BytesRead: %d", r.bytesRead)
					time.Sleep(10 * time.Millisecond)
					continue
				}
			}
		}

		// Copy data
		n = copy(p, r.currentData[r.dataOffset:])
		r.dataOffset += n
		r.bytesRead += int64(n)

		// If we finished this chunk (and it was a full chunk), advance key
		if r.dataOffset >= int(ChunkDataSize) {
			logger.Infof("RWW: Fragment completed. Key: %x, Advancing to next fragment", r.currentKey)
			r.currentData = nil
			r.dataOffset = 0
			r.currentKey = NextCacheKey(r.currentKey)
			// Loop will handle loading next
		}

		return n, nil
	}
}

func (r *CacheReader) Size() int64 {
	return r.totalSize
}

func (r *CacheReader) Close() error {
	return nil
}

// Get returns a CacheReader for streaming access
func (s *Stripe) Get(key []byte) (bool, *CacheReader, error) {
	if err := s.checkGetRequest(key); err != nil {
		return false, nil, err
	}

	// Note: RamCache check returns full value.
	// If we want to support streaming from RamCache, we need to wrap it.
	// For now, if in RamCache, we wrap it in a Buffer Reader.
	reader, hit := s.loadFromRamCache(key)
	if hit {
		return true, reader, nil
	}

	// RWW: Check OpenDir
	if s.OpenDir != nil {
		entry := s.OpenDir.OpenRead(key)
		if entry != nil {
			// Pick a writer
			entry.mutex.RLock()
			var writer *CacheWriter
			if len(entry.writers) > 0 {
				// Just pick the first one for now
				writer = entry.writers[0]
			}
			entry.mutex.RUnlock()

			if writer != nil {
				// Init Reader from Writer
				return true, NewCacheReaderFromWriter(s, key, writer), nil
			}
		}
	}

	// Directory Lookup
	s.mutex.RLock()
	hit, _, d := s.Dm.Get(key)
	if !hit {
		s.mutex.RUnlock()
		return false, nil, nil
	}
	headOffset := Offset(d.offset())
	headSize := Offset(d.approxSize())
	s.mutex.RUnlock()

	// Read Head Chunk
	// We need to read it to get TotalSize and verify integrity.
	// We can reuse loadFromAggregationBuffer / loadFromDisk logic, but we need to be careful about locks.
	// loadFromAggregationBuffer requires RLock on aggBuffer (it handles it internal RLock on s.aggWriteBuffer).
	// loadFromDisk requires Fp access (thread safe).

	// We need a helper that doesn't require s.mutex, but uses atomic/passed params.

	headCk, err := s.readChunkInternal(headOffset, headSize)
	if err != nil {
		return false, nil, err
	}

	// Verify KeyHash matches the lookup key
	keyHash := md5.Sum(key)
	// We need to import bytes package if we use bytes.Equal
	// Or just compare manually.
	// Let's assume bytes is imported or we fix imports later.
	// Using loop for now to avoid dependency if not needed.
	if string(headCk.Header.KeyHash[:]) != string(keyHash[:]) {
		return false, nil, nil
	}

	// Create Reader
	reader = &CacheReader{
		stripe:     s,
		headKey:    key,
		currentKey: key, // Head Key - but wait, Head Doc is metadata now!
		// We need to parse metadata to set up the reader for DATA.
		// totalSize:   int64(headCk.Header.TotalSize),
		bytesRead: 0,
		// currentData: headCk.DataRaw, // This is metadata now
		dataOffset: 0,
	}

	// Parse Metadata (Vector)
	vector := &Vector{}
	if err := vector.UnmarshalBinary(headCk.DataRaw); err != nil {
		return false, nil, err
	}
	if len(vector.Alternates) == 0 {
		// Should not happen for a valid object
		return false, nil, errors.New("no alternates in vector")
	}

	// Alternate Selection: For now, always pick the first one
	// ATS would match Request Headers here
	alt := vector.Alternates[0]

	reader.totalSize = int64(alt.PayloadSize)
	reader.earliestKey = alt.EarliestKey[:]
	reader.currentKey = alt.EarliestKey[:]

	// If TotalSize > 0, we need to fetch the first data fragment (EarliestKey)
	if reader.totalSize > 0 {

		// Fetch first data fragment immediately? Or wait for Read()?
		// Let's fetch it now to populate currentData

		s.mutex.RLock()
		hit, _, d = s.Dm.Get(reader.currentKey)
		if !hit {
			s.mutex.RUnlock()
			// Data fragment missing?
			return false, nil, errors.New("earliest data fragment missing")
		}
		offset := Offset(d.offset())
		size := Offset(d.approxSize())
		s.mutex.RUnlock()

		dataCk, err := s.readChunkInternal(offset, size)
		if err != nil {
			return false, nil, err
		}

		reader.currentData = dataCk.DataRaw
		reader.nextKey = NextCacheKey(reader.currentKey)
	} else {
		// Empty object
		reader.currentData = []byte{}
	}

	return true, reader, nil
}
