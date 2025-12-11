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
	Key    []byte

	// Metadata
	Info *CacheHTTPInfo

	// State
	offset int64        // Read offset (relative to Data start of CURRENT fragment)
	writer *CacheWriter // If reading from a writer (RWW)

	// Multi-fragment state
	fragmentIndex int
	totalRead     int64 // Total payload bytes read across all fragments
	currentDocLen int64 // Length of data in current fragment

	// Internal
	doc       *Doc
	docOffset int64  // Offset of Doc on disk
	ramData   []byte // Data from RamCache

	mutex sync.Mutex
}

func NewCacheReader(s *Stripe, key []byte) *CacheReader {
	return &CacheReader{
		Stripe: s,
		Key:    key,
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

	// Use loadFragment(0) to consistently load everything including Doc, Info, and currentDocLen
	if err := r.loadFragment(0); err != nil {
		return nil, err
	}

	// Info should be populated by loadFragment -> parse Doc
	// Wait, loadFragment parses Doc, but does it parse Info?
	// loadFragment in previous implementation only parsed Doc Header.
	// We need to ensure it parses Info too if requested, or we parse it here.

	// loadFragment logic:
	// It reads DocHeader.
	// It DOES NOT currently read Info explicitly into r.Info.
	// But it has the data (ramData or can read disk).

	// Let's enhance loadFragment to optionally load Info, or parse it here.
	// Since r.doc is set, we can parse Info.

	if r.doc != nil && r.doc.HLen > 0 {
		if r.ramData != nil {
			// Parse from Ram
			// [DocHeader][Info][Data]
			if len(r.ramData) >= DocHeaderSize+int(r.doc.HLen) {
				r.Info = NewCacheHTTPInfo()
				infoBytes := r.ramData[DocHeaderSize : DocHeaderSize+int(r.doc.HLen)]
				if err := r.Info.UnmarshalBinary(infoBytes); err != nil {
					return nil, err
				}
			}
		} else if r.Stripe.Fd != nil {
			// Parse from Disk
			infoBuf := make([]byte, r.doc.HLen)
			if _, err := r.Stripe.Fd.ReadAt(infoBuf, r.docOffset+int64(DocHeaderSize)); err != nil {
				return nil, err
			}
			r.Info = NewCacheHTTPInfo()
			if err := r.Info.UnmarshalBinary(infoBuf); err != nil {
				return nil, err
			}
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

	// Ensure current doc is loaded
	if r.doc == nil {
		// Initial load (Fragment 0)
		if err := r.loadFragment(0); err != nil {
			return 0, err
		}
	}

	totalBytesRead := 0

	for totalBytesRead < len(p) {
		// Calculate available data in current fragment
		remainingInFrag := r.currentDocLen - r.offset

		if remainingInFrag <= 0 {
			// End of this fragment.
			// Check if there are more fragments.
			if uint64(r.totalRead) >= r.doc.TotalLen {
				// End of object
				if totalBytesRead == 0 {
					return 0, io.EOF
				}
				return totalBytesRead, nil
			}

			// Load next fragment
			r.fragmentIndex++
			if err := r.loadFragment(r.fragmentIndex); err != nil {
				// Failed to load next fragment
				if totalBytesRead > 0 {
					return totalBytesRead, nil // Return what we have?
				}
				return 0, err
			}
			r.offset = 0 // Reset offset for new fragment
			// Recalculate remainingInFrag for new fragment
			remainingInFrag = r.currentDocLen
		}

		toRead := int64(len(p) - totalBytesRead)
		if toRead > remainingInFrag {
			toRead = remainingInFrag
		}

		// Perform Read
		var nRead int
		if r.ramData != nil {
			// Read from Ram
			// ramData contains [Header][Info][Data]
			headerSize := int64(DocHeaderSize) + int64(r.doc.HLen)
			if int64(len(r.ramData)) < headerSize+r.offset+toRead {
				// Should not happen if RamCache data is valid
				return totalBytesRead, fmt.Errorf("ram data short read")
			}
			src := r.ramData[headerSize+r.offset : headerSize+r.offset+toRead]
			nRead = copy(p[totalBytesRead:], src)
		} else {
			// Read from Disk
			readPos := r.docOffset + int64(DocHeaderSize) + int64(r.doc.HLen) + r.offset
			nRead, err = r.Stripe.Fd.ReadAt(p[totalBytesRead:totalBytesRead+int(toRead)], readPos)
			if nRead == 0 && err != nil {
				if totalBytesRead > 0 {
					return totalBytesRead, nil
				}
				return 0, err
			}
		}

		r.offset += int64(nRead)
		r.totalRead += int64(nRead)
		totalBytesRead += nRead

		if err != nil {
			break
		}
	}

	return totalBytesRead, nil
}

func (r *CacheReader) loadFragment(idx int) error {
	// Determine Key
	currentKey := FragmentKey(r.Key, idx)

	// Pad currentKey to 16 bytes for comparison with Doc.Key
	var keyPadded [16]byte
	copy(keyPadded[:], currentKey)

	// Clear current state
	r.ramData = nil

	// 1. Check RamCache
	if r.Stripe.RamCache != nil {
		if val, ok := r.Stripe.RamCache.Get(currentKey); ok {
			r.ramData = val

			// If this is a new fragment, we need to parse the Doc struct
			// Only allocate new Doc if we don't have one, or reuse existing one but parse new data
			// To rely on TotalLen from fragment 0, we should preserve the original Doc or TotalLen?
			// ATS stores TotalLen in the First Fragment.
			// Subsequent fragments might not have valid TotalLen or repeat it.
			// Our CacheWriter puts TotalLen in ALL fragments now (streaming update).
			// But wait, CacheWriter.writeFragment sets TotalLen = w.totalWritten.
			// For intermediate fragments (1..N), w.totalWritten is cumulative SO FAR.
			// Only the LAST fragment and Fragment 0 (written last) have the FINAL TotalLen.
			// Reader logic relies on r.doc.TotalLen.
			// If we load Fragment 1, and it says TotalLen=1MB (cumulative), but object is 10MB.
			// Reader checks: `if uint64(r.totalRead) >= r.doc.TotalLen`.
			// If we overwrite r.doc with Fragment 1's header, and it has partial TotalLen, we might stop early?
			// Correct behavior: We should trust Fragment 0's TotalLen.
			// So, we should only parse Doc header to get `Len` and `HLen` for the current fragment,
			// but PRESERVE the `TotalLen` from Fragment 0?

			// Let's decode into a temporary doc struct first.
			tempDoc := &Doc{}

			if len(val) >= DocHeaderSize {
				if err := tempDoc.UnmarshalBinary(val[:DocHeaderSize]); err != nil {
					return err
				}
				// Check Key
				if !bytes.Equal(tempDoc.Key[:], keyPadded[:]) {
					// Key Mismatch (Collision or Stale)
					r.ramData = nil
					// Fallback to disk
				} else {
					// Valid Ram Hit
					if idx == 0 {
						r.doc = tempDoc // First fragment is authoritative
					} else {
						// Update current fragment properties
						// We trust r.doc.TotalLen from Frag 0
						// Update r.doc.Len, HLen for calculation of data offset
						r.doc.Len = tempDoc.Len
						r.doc.HLen = tempDoc.HLen
						// r.doc.Key = tempDoc.Key
					}

					r.currentDocLen = int64(tempDoc.Len) - int64(DocHeaderSize) - int64(tempDoc.HLen)
					return nil
				}
			}
		}
	}

	// 2. Check Disk
	if r.Stripe.DirMgr != nil {
		hit, _, d := r.Stripe.DirMgr.Get(currentKey)
		if !hit {
			return io.EOF
		}
		r.docOffset = int64(d.offset())

		if r.Stripe.Fd == nil {
			return fmt.Errorf("stripe fd closed")
		}

		headerBuf := make([]byte, DocHeaderSize)
		if _, err := r.Stripe.Fd.ReadAt(headerBuf, r.docOffset); err != nil {
			return err
		}

		tempDoc := &Doc{}
		if err := tempDoc.UnmarshalBinary(headerBuf); err != nil {
			return err
		}

		// Verify Magic
		if tempDoc.Magic != DocMagic {
			return fmt.Errorf("invalid doc magic at offset %d: %x", r.docOffset, tempDoc.Magic)
		}

		// Verify Key (To detect hash collision in DirMgr)
		if !bytes.Equal(tempDoc.Key[:], keyPadded[:]) {
			return fmt.Errorf("doc key mismatch at offset %d: expected %x, got %x", r.docOffset, keyPadded, tempDoc.Key)
		}

		if idx == 0 {
			r.doc = tempDoc
		} else {
			r.doc.Len = tempDoc.Len
			r.doc.HLen = tempDoc.HLen
		}

		r.currentDocLen = int64(tempDoc.Len) - int64(DocHeaderSize) - int64(tempDoc.HLen)
		return nil
	}

	return io.EOF
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

// Deprecated/Internal helper removed in favor of loadFragment
func (r *CacheReader) loadDocAndInfoLocked() error {
	return r.loadFragment(0)
}

func (r *CacheReader) readFromRam(p []byte) (n int, err error) {
	// Should be handled in Read loop now
	return 0, nil
}
