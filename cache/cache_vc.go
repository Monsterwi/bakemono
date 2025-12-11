package cache

import (
	"fmt"
)

// CacheVC (Cache Virtual Connection) acts as the interface for reading and writing cache objects.
// It manages the lifecycle of a cache operation.
// Corresponds to CacheVC in ATS.
type CacheVC struct {
	// Link to the underlying Stripe
	Stripe *Stripe

	// The key being operated on
	Key []byte

	// Read/Write State
	reading bool
	writing bool
	closed  bool

	// Underlying components
	reader *CacheReader
	writer *CacheWriter

	// Stats / Info
	Info *CacheHTTPInfo
}

// NewCacheVCRead creates a CacheVC for reading.
func NewCacheVCRead(stripe *Stripe, key []byte) (*CacheVC, error) {
	// Check RWW first (via Stripe or Cache helper)
	// We will reuse the logic in Cache.OpenRead basically, but encapsulated here.
	// For now, let's assume the caller (Cache.OpenRead) sets up the reader.

	// Actually, CacheVC IS the thing returned by OpenRead in ATS.
	// So we should move the setup logic here or have a factory.

	vc := &CacheVC{
		Stripe:  stripe,
		Key:     key,
		reading: true,
	}

	// Initialize Reader
	// Check OpenDir for RWW
	stripe.mu.RLock()
	od := stripe.OpenDir.OpenRead(key)
	stripe.mu.RUnlock()

	if od != nil {
		od.mutex.RLock()
		// Check if there are active writers
		if len(od.writers) > 0 {
			// RWW
			vc.reader = NewCacheReaderFromWriter(stripe, key, od.writers[0])
		}
		od.mutex.RUnlock()
	}

	if vc.reader == nil {
		// Disk/Ram
		vc.reader = NewCacheReader(stripe, key)
	}

	// We should probably try to load the header immediately to verify existence?
	// ATS CacheVC does do_io_read which initiates the state machine.
	// Here we are synchronous.

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
	stripe.mu.Lock()
	stripe.OpenDir.OpenWrite(key, vc.writer)
	stripe.mu.Unlock()

	return vc, nil
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
	}
}

// GetHTTPInfo gets the HTTP metadata.
func (vc *CacheVC) GetHTTPInfo() (*CacheHTTPInfo, error) {
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

// Deref? ATS uses ref-counting.
// Go GC handles memory, but we might need explicit cleanup for OpenDir registration if Close() is missed?
// Usually user calls Close().
