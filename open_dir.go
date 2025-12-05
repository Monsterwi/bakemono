package bakemono

import (
	"sync"
)

// OpenDirEntry represents a directory entry for a document that is currently being written.
// Ref: trafficserver/src/iocore/cache/P_CacheInternal.h (OpenDirEntry)
type OpenDirEntry struct {
	mutex sync.RWMutex

	// List of writers for this document.
	// In ATS this is a linked list of CacheVC.
	// Here we store pointers to CacheWriter.
	writers []*CacheWriter

	// Helper to manage single writer optimization or just list
	// For simplicity, we use a slice.
}

func NewOpenDirEntry() *OpenDirEntry {
	return &OpenDirEntry{
		writers: make([]*CacheWriter, 0),
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
// Returns true if successful, false if write conflict (if we enforce single writer).
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
	entry.writers = append(entry.writers, w)
	entry.mutex.Unlock()

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

	entry, ok := od.entries[string(key)]
	if !ok {
		return
	}

	entry.mutex.Lock()
	defer entry.mutex.Unlock()

	// Remove w from writers
	for i, writer := range entry.writers {
		if writer == w {
			// Remove element
			entry.writers = append(entry.writers[:i], entry.writers[i+1:]...)
			break
		}
	}

	// If empty, remove entry from map
	if len(entry.writers) == 0 {
		delete(od.entries, string(key))
	}
}
