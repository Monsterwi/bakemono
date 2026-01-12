package cache

import (
	"fmt"
	"time"
)

// CacheVC (Cache Virtual Connection) acts as the interface for reading and writing cache objects.
// It manages the lifecycle of a cache operation.
// Corresponds to CacheVC in ATS.
type CacheVC struct {
	// Link to the underlying Stripe
	Stripe *Stripe

	// The key being operated on
	Key []byte

	// Read/Write/Remove State
	reading  bool
	writing  bool
	removing bool
	closed   bool

	// Underlying components
	reader *CacheReader
	writer *CacheWriter

	// Stats / Info
	Info *CacheHTTPInfo
}

// NewCacheVCRead creates a CacheVC for reading.
func NewCacheVCRead(stripe *Stripe, key []byte) (*CacheVC, error) {
	vc := &CacheVC{
		Stripe:  stripe,
		Key:     key,
		reading: true,
	}

	// 1. Check OpenDir for Active Writers (RWW)
	entry := stripe.OpenDir.Get(key)
	if entry != nil {
		// RWW
		vc.reader = NewCacheReaderFromWriter(stripe, key, entry.Writer)
	}

	if vc.reader == nil {
		// Disk/Ram
		vc.reader = NewCacheReader(stripe, key)
		if vc.reader == nil {
			return nil, fmt.Errorf("no reader found for key: %s", key)
		}
	}

	return vc, nil
}

// NewCacheVCWrite creates a CacheVC for writing.
func NewCacheVCWrite(stripe *Stripe, key []byte) (*CacheVC, error) {
	vc := &CacheVC{
		Stripe:  stripe,
		Key:     key,
		writing: true,
	}

	vc.writer = NewCacheWriter(stripe, key)

	// Register with OpenDir
	entry, isLeader := stripe.OpenDir.Add(key, vc.writer)
	if !isLeader {
		return nil, fmt.Errorf("write conflict: another writer is active")
	}
	vc.writer.od = entry

	return vc, nil
}

// IsRWW returns true if this VC is reading from an active writer.
func (vc *CacheVC) IsRWW() bool {
	return vc.reading && vc.reader != nil && vc.reader.writer != nil
}

// WaitForHeaders waits for the writer to receive headers from upstream.
// This is only used for RWW readers.
func (vc *CacheVC) WaitForHeaders(timeout time.Duration) error {
	if !vc.IsRWW() {
		return nil
	}

	w := vc.reader.writer
	// Need to access the OpenDirEntry via the writer to get the signal
	if w == nil || w.od == nil {
		return fmt.Errorf("invalid RWW state: no writer or opendir entry")
	}

	select {
	case <-w.od.ReadySignal:
		// Headers are ready (or writer closed/aborted)
		// We should check if Info is actually set
		if w.Info == nil {
			return fmt.Errorf("writer finished without headers")
		}
		// Refresh our info from the writer
		vc.Info = w.Info
		vc.reader.Info = w.Info
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timeout waiting for headers")
	}
}

// Read reads data from the cache.
func (vc *CacheVC) Read(p []byte) (n int, err error) {
	if !vc.reading || vc.closed {
		return 0, fmt.Errorf("read operation not active or closed")
	}
	return vc.reader.Read(p)
}

// Write writes data to the cache.
func (vc *CacheVC) Write(p []byte) (n int, err error) {
	if !vc.writing || vc.closed {
		return 0, fmt.Errorf("write operation not active or closed")
	}
	return vc.writer.Write(p)
}

// Close closes the CacheVC and underlying reader/writer.
func (vc *CacheVC) Close() error {
	if vc.closed {
		return nil
	}

	var err error
	if vc.writing && vc.writer != nil {
		err = vc.writer.Close()
		// Writer finished, decrement RefCount in OpenDir
		vc.Stripe.OpenDir.Remove(vc.Key)
	}

	// If this was an RWW reader (holding a reference to a writer via OpenDir),
	// we must decrement the RefCount when we are done reading.
	if vc.reading && vc.reader != nil && vc.reader.writer != nil {
		vc.Stripe.OpenDir.Remove(vc.Key)
	}

	// Reader doesn't strictly need closing in our current impl (no fd held permanently),
	// but good for cleanup if we add resource tracking.

	vc.closed = true
	return err
}

// SetHTTPInfo sets the HTTP metadata for writing.
func (vc *CacheVC) SetHTTPInfo(info *CacheHTTPInfo) {
	if vc.writing && vc.writer != nil {
		vc.writer.Info = info
		vc.Info = info

		// Notify listeners that headers are ready!
		if vc.writer.od != nil {
			// Only close if not already closed
			select {
			case <-vc.writer.od.ReadySignal:
				// Already closed
			default:
				close(vc.writer.od.ReadySignal)
			}
		}
	}
}

// LoadHTTPInfo loads the HTTP metadata from disk or RamCache if not already loaded.
// This is a convenience method that delegates to the underlying reader.
func (vc *CacheVC) LoadHTTPInfo() (*CacheHTTPInfo, error) {
	if vc.reading && vc.reader != nil {
		info, err := vc.reader.LoadHTTPInfo()
		if err != nil {
			return nil, err
		}
		vc.Info = info
		return info, nil
	}
	if vc.writing && vc.writer != nil {
		return vc.writer.Info, nil
	}
	return nil, fmt.Errorf("no active operation")
}

// NewCacheVCRemove creates a CacheVC for removing.
func NewCacheVCRemove(stripe *Stripe, key []byte) (*CacheVC, error) {
	vc := &CacheVC{
		Stripe:   stripe,
		Key:      key,
		removing: true,
	}

	return vc, nil
}

// Remove removes the cache entry and all its fragments.
func (vc *CacheVC) Remove() error {
	if !vc.removing || vc.closed {
		return fmt.Errorf("remove operation not active or closed")
	}

	// Step 1: Abort active writers
	// Check if there is an active writer for this key and mark it to not update directory
	hadActiveWriter := false
	entry := vc.Stripe.OpenDir.Get(vc.Key)
	if entry != nil {
		// We'll just delete it from OpenDir to prevent it from updating
		vc.Stripe.OpenDir.ForceRemove(vc.Key)
		hadActiveWriter = true
	}

	// Step 2: Remove all fragments
	// Start with fragment 0 (uses original key)
	fragmentIndex := 0
	removedCount := 0

	for {
		// Generate fragment key
		fragmentKey := FragmentKey(vc.Key, fragmentIndex)

		// Try to remove this fragment
		removed, err := vc.removeFragment(fragmentKey)
		if err != nil {
			// If this is fragment 0 and it failed, return error
			if fragmentIndex == 0 {
				return fmt.Errorf("failed to remove fragment 0: %w", err)
			}
			// For subsequent fragments, if not found, we've reached the end
			break
		}

		if removed {
			removedCount++
		} else {
			// No more fragments
			break
		}

		fragmentIndex++
	}

	// If we had an active writer but no fragments in directory, that's OK
	// (the writer hadn't committed yet). Otherwise, we should have removed at least one fragment.
	if removedCount == 0 && !hadActiveWriter {
		return fmt.Errorf("no fragments removed")
	}

	return nil
}

// removeFragment removes a single fragment.
// Remove operation only deletes the directory entry, not the actual disk content.
// The disk space will be reclaimed when overwritten by new data (circular write).
func (vc *CacheVC) removeFragment(key []byte) (bool, error) {
	// Step 1: Probe directory for the key
	hit, _, _ := vc.Stripe.DirMgr.Get(key)

	if !hit {
		// Fragment not found - this is OK for fragments > 0
		return false, nil
	}

	// Step 2: Remove from directory
	// No need to read/verify disk content - just remove the index
	_, err := vc.Stripe.DirMgr.Delete(key)
	if err != nil {
		return false, fmt.Errorf("failed to remove from directory: %w", err)
	}

	// Step 3: Remove from RamCache
	if vc.Stripe.RamCache != nil {
		vc.Stripe.RamCache.Delete(key)
	}

	return true, nil
}
