package cache

import (
	"sync"
)

// CacheWriter represents a writer for a cache object.
// Stub for compatibility with OpenDir.
// Moved to cache_write.go

// OpenDirEntry represents a directory entry for a document that is currently being written.
// Ref: trafficserver/src/iocore/cache/P_CacheInternal.h (OpenDirEntry)
type OpenDirEntry struct {
	mutex sync.RWMutex

	// List of writers for this document.
	// In ATS this is a linked list of CacheVC.
	// Here we store pointers to CacheWriter.
	writers []*CacheWriter

	// Flag to indicate if a writer is currently active (exclusive write lock)
	hasWriter bool
}

func NewOpenDirEntry() *OpenDirEntry {
	return &OpenDirEntry{
		writers:   make([]*CacheWriter, 0),
		hasWriter: false,
	}
}

// OpenDir manages concurrent access to documents being written.
// Ref: trafficserver/src/iocore/cache/P_CacheInternal.h (OpenDir)
type OpenDir struct {
	mutex   sync.RWMutex
	entries map[string]*OpenDirEntry
}

func NewOpenDir() *OpenDir {
	return &OpenDir{
		entries: make(map[string]*OpenDirEntry),
	}
}

// OpenWrite attempts to register a writer for a key.
// Returns true if successful, false if write conflict (enforce single writer).
func (od *OpenDir) OpenWrite(key []byte, w *CacheWriter) bool {
	k := string(key)
	od.mutex.Lock()
	defer od.mutex.Unlock()

	entry, ok := od.entries[k]
	if !ok {
		entry = NewOpenDirEntry()
		od.entries[k] = entry
	}

	entry.mutex.Lock()
	defer entry.mutex.Unlock()

	// Strict single-writer policy:
	// If there is already a writer, we reject the new writer.
	// In ATS, this might queue the writer or return failure.
	// Here we return false to indicate conflict.
	if entry.hasWriter {
		return false
	}

	entry.writers = append(entry.writers, w)
	entry.hasWriter = true

	// Link the writer to this entry so it can remove itself later
	w.od = entry

	return true
}

// OpenRead attempts to find an existing writer for a key.
func (od *OpenDir) OpenRead(key []byte) *OpenDirEntry {
	od.mutex.RLock()
	defer od.mutex.RUnlock()

	return od.entries[string(key)]
}

// CloseWrite removes a writer.
func (od *OpenDir) CloseWrite(key []byte, w *CacheWriter) {
	od.mutex.Lock()
	defer od.mutex.Unlock()

	k := string(key)
	entry, ok := od.entries[k]
	if !ok {
		return
	}

	entry.mutex.Lock()
	defer entry.mutex.Unlock()

	// Remove w from writers
	found := false
	for i, writer := range entry.writers {
		if writer == w {
			// Remove element
			entry.writers = append(entry.writers[:i], entry.writers[i+1:]...)
			found = true
			break
		}
	}

	if found {
		// If we removed the active writer, clear the flag
		// (Assuming w was the active writer, which it should be if we enforce single writer)
		entry.hasWriter = false
	}

	// If empty, remove entry from map
	if len(entry.writers) == 0 {
		delete(od.entries, k)
	}
}
