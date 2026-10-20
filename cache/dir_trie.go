package cache

import (
	"errors"
	"sort"
	"strings"
	"sync"
)

const (
	MaxIndexKeyBytes = 16 << 10
	MaxIndexBytes    = 16 << 20
	MaxIndexEntries  = 65536
)

var ErrIndexFull = errors.New("cache identity index capacity exceeded")

type IndexEntry struct{ Key, Selector string }

// DirTrie maps full representation keys to URL selectors. Both indexes are bounded.
type DirTrie struct {
	mu              sync.RWMutex
	entries         map[string]string
	selectors       map[string]map[string]struct{}
	capacity, bytes int
}

func NewDirTrie(capacity ...int) *DirTrie {
	limit := MaxIndexEntries
	if len(capacity) > 0 && capacity[0] < limit {
		limit = capacity[0]
	}
	if limit < 0 {
		limit = 0
	}
	return &DirTrie{entries: make(map[string]string), selectors: make(map[string]map[string]struct{}), capacity: limit}
}

func validateIdentity(key, selector []byte) error {
	if len(key) == 0 || len(selector) == 0 || len(key) > MaxIndexKeyBytes || len(selector) > MaxIndexKeyBytes {
		return errors.New("invalid cache identity length")
	}
	return nil
}

func (dt *DirTrie) Set(key []byte) { _ = dt.SetIdentity(key, key) }

func (dt *DirTrie) SetIdentity(key, selector []byte) error {
	if err := validateIdentity(key, selector); err != nil {
		return err
	}
	dt.mu.Lock()
	defer dt.mu.Unlock()
	k, s := string(key), string(selector)
	old, exists := dt.entries[k]
	size := dt.bytes + len(k) + len(s)
	if exists {
		size -= len(k) + len(old)
	}
	if (!exists && len(dt.entries) >= dt.capacity) || size > MaxIndexBytes {
		return ErrIndexFull
	}
	if exists {
		dt.deleteLocked(k)
	}
	dt.entries[k] = s
	if dt.selectors[s] == nil {
		dt.selectors[s] = make(map[string]struct{})
	}
	dt.selectors[s][k] = struct{}{}
	dt.bytes = size
	return nil
}

func (dt *DirTrie) Get(prefix []byte) []string {
	keys, _ := dt.KeysForPrefixLimit(prefix, MaxIndexEntries)
	return keys
}

func (dt *DirTrie) KeysForPrefixLimit(prefix []byte, limit int) ([]string, error) {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	var keys []string
	for selector, entries := range dt.selectors {
		if strings.HasPrefix(selector, string(prefix)) {
			if len(keys)+len(entries) > limit {
				return nil, ErrIndexFull
			}
			for key := range entries {
				keys = append(keys, key)
			}
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (dt *DirTrie) KeysForSelector(selector []byte) []string {
	keys, _ := dt.KeysForSelectorLimit(selector, MaxIndexEntries)
	return keys
}

func (dt *DirTrie) KeysForSelectorLimit(selector []byte, limit int) ([]string, error) {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	entries := dt.selectors[string(selector)]
	if len(entries) > limit {
		return nil, ErrIndexFull
	}
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}

func (dt *DirTrie) Delete(key []byte) {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	dt.deleteLocked(string(key))
}

func (dt *DirTrie) deleteLocked(key string) {
	if selector, ok := dt.entries[key]; ok {
		dt.bytes -= len(key) + len(selector)
		delete(dt.entries, key)
		delete(dt.selectors[selector], key)
		if len(dt.selectors[selector]) == 0 {
			delete(dt.selectors, selector)
		}
	}
}

func (dt *DirTrie) Snapshot() []IndexEntry {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	entries := make([]IndexEntry, 0, len(dt.entries))
	for key, selector := range dt.entries {
		entries = append(entries, IndexEntry{Key: key, Selector: selector})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return entries
}

// Restore validates the whole replacement before changing the live index.
func (dt *DirTrie) Restore(entries []IndexEntry) error {
	replacement := NewDirTrie(dt.capacity)
	for _, entry := range entries {
		if _, exists := replacement.entries[entry.Key]; exists {
			return errors.New("duplicate cache identity")
		}
		if err := replacement.SetIdentity([]byte(entry.Key), []byte(entry.Selector)); err != nil {
			return err
		}
	}
	dt.mu.Lock()
	defer dt.mu.Unlock()
	dt.entries, dt.selectors, dt.bytes = replacement.entries, replacement.selectors, replacement.bytes
	return nil
}
