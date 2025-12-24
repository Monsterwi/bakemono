package cache

import (
	"bytes"
	"fmt"
	"io"
	"sync"
)

// CacheReader handles reading data from the cache.
// It supports reading from memory (RamCache), Write Buffer (RWW), and Disk.
type CacheReader struct {
	Stripe *Stripe

	Key, FirstKey []byte
	Dir           Dir

	// Metadata
	Info *CacheHTTPInfo

	// State
	offset int64        // Read offset (relative to Data start of CURRENT fragment)
	writer *CacheWriter // If reading from a writer (RWW)

	// Multi-fragment state
	fragmentIndex int
	totalRead     int64  // Total payload bytes read across all fragments
	totalLen      uint64 // Total length from Fragment 0's Doc (cached)

	// Internal
	buf []byte // Buffer for reading data (contains [DocHeader][Info][Data])

	mutex sync.Mutex
}

// parseDocFromBuf parses Doc from r.buf. This is a helper to avoid storing Doc as state.
func (r *CacheReader) parseDocFromBuf() (*Doc, error) {
	if len(r.buf) < DocHeaderSize {
		return nil, fmt.Errorf("buffer too small for doc header")
	}

	doc := &Doc{}
	if err := doc.UnmarshalBinary(r.buf[:DocHeaderSize]); err != nil {
		return nil, err
	}

	return doc, nil
}

func NewCacheReader(s *Stripe, key []byte) *CacheReader {
	// Initialize with Key. Dir lookup is done lazily or in loadFragment.
	// But per ATS requirement, we should init these if possible.
	// However, usually we need to lock to lookup.
	// Let's copy key.
	k := make([]byte, len(key))
	copy(k, key)
	return &CacheReader{
		Stripe:   s,
		Key:      k,
		FirstKey: k,
	}
}

// NewCacheReaderFromWriter creates a reader that reads from an active writer (RWW).
func NewCacheReaderFromWriter(s *Stripe, key []byte, w *CacheWriter) *CacheReader {
	return &CacheReader{
		Stripe: s,
		Key:    key,
		writer: w,
		Info:   w.Info, // Share info from writer
	}
}

// LoadHTTPInfo reads the metadata from disk or RamCache if not already loaded.
func (r *CacheReader) LoadHTTPInfo() (*CacheHTTPInfo, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if r.Info != nil {
		return r.Info, nil
	}

	if r.writer != nil {
		return r.writer.Info, nil
	}

	if err := r.loadFragment(0); err != nil {
		return nil, err
	}

	// Parse Doc to get HLen
	doc, err := r.parseDocFromBuf()
	if err != nil {
		return nil, err
	}

	// Parse Info from r.buf
	// r.buf contains [DocHeader][Info][Data]
	if doc.HLen > 0 {
		infoStart := DocHeaderSize
		infoEnd := DocHeaderSize + int(doc.HLen)
		if len(r.buf) >= infoEnd {
			r.Info = NewCacheHTTPInfo()
			if err := r.Info.UnmarshalBinary(r.buf[infoStart:infoEnd]); err != nil {
				return nil, err
			}
		} else {
			return nil, fmt.Errorf("buffer too small for http info")
		}
	} else {
		r.Info = NewCacheHTTPInfo() // Empty
	}

	return r.Info, nil
}

func (r *CacheReader) Read(p []byte) (n int, err error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	// RWW Path (Assumes single fragment in buffer usually, or simpler stream)
	if r.writer != nil {
		return r.readFromWriter(p)
	}

	// Ensure current fragment is loaded
	if len(r.buf) == 0 {
		// Initial load (Fragment 0)
		if err := r.loadFragment(0); err != nil {
			return 0, err
		}
		r.fragmentIndex = 0 // Mark that we've loaded fragment 0
	}

	totalBytesRead := 0

	for totalBytesRead < len(p) {
		// Ensure current fragment data is available in buffer
		if len(r.buf) == 0 {
			// Check if we have read everything
			if r.totalLen > 0 && uint64(r.totalRead) >= r.totalLen {
				if totalBytesRead == 0 {
					return 0, io.EOF
				}
				return totalBytesRead, nil
			}

			// Load next fragment
			r.fragmentIndex++
			if err := r.loadFragment(r.fragmentIndex); err != nil {
				if totalBytesRead > 0 {
					return totalBytesRead, nil
				}
				return 0, err
			}
			r.offset = 0 // Reset offset for new fragment
		}

		// Parse Doc to get HLen for data offset calculation
		doc, err := r.parseDocFromBuf()
		if err != nil {
			if totalBytesRead > 0 {
				return totalBytesRead, nil
			}
			return 0, err
		}

		// Calculate available data in current fragment (from r.buf)
		// Data starts after DocHeader + HLen
		dataStart := int64(DocHeaderSize) + int64(doc.HLen)
		available := int64(len(r.buf)) - dataStart - r.offset

		if available <= 0 {
			// Current fragment exhausted, check if we've read all data before clearing buffer
			if r.totalLen > 0 && uint64(r.totalRead) >= r.totalLen {
				// All data read, don't clear buffer, just return what we have
				if totalBytesRead == 0 {
					return 0, io.EOF
				}
				return totalBytesRead, nil
			}
			// Need next fragment, clear buffer to trigger reload
			r.buf = nil
			continue
		}

		toRead := int64(len(p) - totalBytesRead)
		if toRead > available {
			toRead = available
		}

		// Perform Memory Copy
		src := r.buf[dataStart+r.offset : dataStart+r.offset+toRead]
		nRead := copy(p[totalBytesRead:], src)

		r.offset += int64(nRead)
		r.totalRead += int64(nRead)
		totalBytesRead += nRead
	}

	return totalBytesRead, nil
}

// loadFragment performs the directory lookup and data load pipeline.
// Logic flow aligned with ATS CacheVC::handleRead.
func (r *CacheReader) loadFragment(idx int) error {
	// 1. Determine Key
	currentKey := FragmentKey(r.FirstKey, idx)
	r.Key = currentKey

	// 2. Lookup Dir (if not already set for idx 0)
	// ATS typically does this before handleRead.
	// For idx=0, we might have dir from OpenRead? But r.Dir might be empty initially.
	// We need to lookup every time Key changes.
	hit, _, d := r.Stripe.DirMgr.Get(r.Key)
	if !hit {
		return fmt.Errorf("fragment %d missing", idx)
	}
	r.Dir = d

	// 3. Handle Read (RAM -> Agg -> Disk)
	if err := r.handleRead(); err != nil {
		return err
	}

	// 4. Parse Doc
	if err := r.parseDoc(); err != nil {
		return err
	}

	// 5. Evacuation Check
	r.checkEvacuation(r.Dir, r.Key)

	return nil
}

func (r *CacheReader) handleRead() error {
	if r.loadFromRamCache() {
		return nil
	}
	if r.loadFromAggregationBuffer() {
		return nil
	}
	return r.loadFromDisk()
}

func (r *CacheReader) loadFromRamCache() bool {
	if r.Stripe.RamCache == nil {
		return false
	}
	val, ok := r.Stripe.RamCache.Get(r.Key)
	if !ok {
		return false
	}
	r.buf = val
	return true
}

func (r *CacheReader) loadFromAggregationBuffer() bool {
	if !r.Stripe.DirAggBufValid(&r.Dir) {
		return false
	}

	// Dir is in range, attempt to read
	offset := int64(r.Dir.offset())
	size := r.Dir.approxSize()
	buf := make([]byte, size)
	n := r.Stripe.AggBuffer.CopyFrom(offset, buf)
	if n > 0 {
		// We got some data. Is it complete?
		// AggBuffer read might be partial if it spans buffers?
		// Assuming simple case for now.
		r.buf = buf[:n]
		return true
	}
	return false
}

func (r *CacheReader) loadFromDisk() error {
	// Read from Disk
	// ATS uses pread with approx size
	if r.Stripe.Fd == nil {
		return fmt.Errorf("stripe fd closed")
	}

	offset := int64(r.Dir.offset())
	size := r.Dir.approxSize()

	// ATS: io.aiocb.aio_nbytes = stripe->skip + stripe->len - io.aiocb.aio_offset; (Clamping)
	// We trust approxSize but should clamp to stripe bounds.

	buf := make([]byte, size)

	n, err := r.Stripe.Fd.ReadAt(buf, offset)
	if err != nil && n == 0 {
		return err
	}
	// Note: ReadAt might return n < size if EOF.
	r.buf = buf[:n]
	return nil
}

// parseDoc parses and validates the Doc header from r.buf.
// For Fragment 0, it caches r.totalLen for subsequent fragment reads.
func (r *CacheReader) parseDoc() error {
	doc, err := r.parseDocFromBuf()
	if err != nil {
		return err
	}

	// Validation
	if doc.Magic != DocMagic {
		return fmt.Errorf("invalid doc magic: %x", doc.Magic)
	}

	// Verify Key
	var keyPadded [16]byte
	copy(keyPadded[:], r.Key)
	if !bytes.Equal(doc.Key[:], keyPadded[:]) {
		return fmt.Errorf("doc key mismatch")
	}

	// Verify FirstKey
	var firstKeyPadded [16]byte
	copy(firstKeyPadded[:], r.FirstKey)
	if !bytes.Equal(doc.FirstKey[:], firstKeyPadded[:]) {
		return fmt.Errorf("doc first_key mismatch")
	}

	// For Fragment 0, cache TotalLen (authoritative source)
	// Fragment 1+ have incorrect TotalLen (cumulative at write time), so we ignore them
	if r.fragmentIndex == 0 {
		r.totalLen = doc.TotalLen
	}

	// Trim buffer to actual length if we read too much (approxSize > real size)
	if int64(len(r.buf)) > int64(doc.Len) {
		r.buf = r.buf[:doc.Len]
	}

	return nil
}

func (r *CacheReader) readFromWriter(p []byte) (n int, err error) {
	r.writer.mutex.RLock()
	defer r.writer.mutex.RUnlock()

	available := int64(len(r.writer.buffer)) - r.offset
	if available <= 0 {
		if r.writer.closed {
			return 0, io.EOF
		}
		return 0, nil
	}

	toRead := int64(len(p))
	if toRead > available {
		toRead = available
	}

	copy(p, r.writer.buffer[r.offset:r.offset+toRead])
	r.offset += toRead

	return int(toRead), nil
}

func (r *CacheReader) checkEvacuation(d Dir, key []byte) {
	objOff := int64(d.offset())

	// Check if within hit evacuate window (aligned with ATS within_hit_evacuate_window)
	if r.Stripe.withinHitEvacuateWindow(objOff) {
		r.Stripe.EvacMgr.Register(key, objOff, int(d.approxSize()))
	}
}
