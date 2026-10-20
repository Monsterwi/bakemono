package cache

import (
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"hash/crc32"

	"github.com/Monsterwi/razor/logger"
)

// Cache is the main entry point, managing multiple stripes (volumes).
// It roughly corresponds to Cache.cc / CacheProcessor in ATS.
type Cache struct {
	Stripes []*Stripe
	Scheme  string // e.g. "http"
}

// NewCache creates a new Cache with nStripes.
// In a real app, this would read from storage.config
func NewCache(nStripes int, paths []string, sizes []int64) (*Cache, error) {
	return NewCacheWithOffsets(nStripes, paths, sizes, nil)
}

// NewCacheWithOffsets creates a new Cache with nStripes, supporting offsets for split spans.
func NewCacheWithOffsets(nStripes int, paths []string, sizes []int64, offsets []int64) (*Cache, error) {
	if len(paths) != len(sizes) || len(paths) < nStripes {
		return nil, fmt.Errorf("mismatch in paths/sizes/stripes")
	}

	c := &Cache{
		Stripes: make([]*Stripe, nStripes),
		Scheme:  "http",
	}

	// Use offsets if provided, otherwise default to 0
	for i := 0; i < nStripes; i++ {
		offset := int64(0)
		if offsets != nil && i < len(offsets) {
			offset = offsets[i]
		}
		c.Stripes[i] = NewStripe(paths[i], sizes[i], offset)
	}

	return c, nil
}

// NewCacheFromStore creates a Cache from a Store using BuildStripeLayout.
func NewCacheFromStore(store *Store, nStripes int) (*Cache, error) {
	paths, sizes, offsets, err := store.BuildStripeLayout(nStripes)
	if err != nil {
		return nil, fmt.Errorf("failed to build stripe layout: %w", err)
	}

	return NewCacheWithOffsets(nStripes, paths, sizes, offsets)
}

func (c *Cache) Init() error {
	for _, s := range c.Stripes {
		// Calculate blocks (assuming 512 sector size for calculation)
		blocks := s.Len / 512
		if err := s.Init(blocks); err != nil {
			return err
		}
		if err := s.Open(); err != nil {
			return err
		}
	}
	return nil
}

func (c *Cache) Close() error {
	for _, s := range c.Stripes {
		if err := s.Close(); err != nil {
			return err
		}
	}
	return nil
}

// SaveAll saves the state of all stripes (Header + Directory) to disk.
// This is useful for graceful shutdown or periodic saves.
func (c *Cache) SaveAll() error {
	for i, s := range c.Stripes {
		if err := s.Save(); err != nil {
			return fmt.Errorf("failed to save stripe %d: %w", i, err)
		}
	}
	return nil
}

// KeyToStripe maps a cache key to a specific stripe.
// ATS uses MD5 hash parts. Here we use a simple modulo on the key.
func (c *Cache) KeyToStripe(key []byte) *Stripe {
	if len(c.Stripes) == 0 {
		return nil
	}
	if len(c.Stripes) == 1 {
		return c.Stripes[0]
	}

	// Simple hash of the key to select stripe
	// ATS uses (key.slice32(2) >> DIR_TAG_WIDTH) % STRIPE_HASH_TABLE_SIZE
	// We'll use CRC32 or similar for simplicity on the whole key
	hash := crc32.ChecksumIEEE(key)
	idx := hash % uint32(len(c.Stripes))
	return c.Stripes[idx]
}

// OpenRead initiates a read operation.
// It returns a CacheVC if successful.
// This matches ATS design where Cache.open_read() returns CacheVC*.
func (c *Cache) OpenRead(key []byte) (*CacheVC, error) {
	stripe := c.KeyToStripe(key)
	if stripe == nil {
		return nil, fmt.Errorf("no stripe found")
	}

	return NewCacheVCRead(stripe, key)
}

// OpenWrite initiates a write operation.
// It returns a CacheVC if successful.
// This matches ATS design where Cache.open_write() returns CacheVC*.
func (c *Cache) OpenWrite(key []byte, opts int) (*CacheVC, error) {
	stripe := c.KeyToStripe(key)
	if stripe == nil {
		return nil, fmt.Errorf("no stripe found")
	}

	return NewCacheVCWrite(stripe, key)
}

// Remove deletes an entry from the cache.
// This corresponds to Cache::remove() in ATS.
// It supports multi-fragment removal automatically.
func (c *Cache) Remove(key []byte) error {
	stripe := c.KeyToStripe(key)
	if stripe == nil {
		return fmt.Errorf("no stripe found")
	}

	vc, err := NewCacheVCRemove(stripe, key)
	if err != nil {
		return err
	}

	return vc.Remove()
}

// PurgeByPrefix removes all cache entries that match the given prefix.
// It returns a list of purged keys.
func (c *Cache) Purge(key []byte) []string {
	// key ends with '/'
	prefixPurge := false
	if key[len(key)-1] == '/' {
		prefixPurge = true
	}

	if !prefixPurge {
		c.Remove(key)
		c.KeyToStripe(key).DirMgr.DirTrie.Delete(key)
		return []string{string(key)}
	}

	// prefix purge
	var purgedKeys []string
	// Search all stripes for keys matching the prefix
	for _, stripe := range c.Stripes {
		if stripe == nil || stripe.DirMgr == nil || stripe.DirMgr.DirTrie == nil {
			continue
		}

		// Get all keys matching the prefix from this stripe's trie
		matchedKeys := stripe.DirMgr.DirTrie.Get(key)
		for _, keyStr := range matchedKeys {
			key := []byte(keyStr)
			// Remove the key from cache
			if err := c.Remove(key); err != nil {
				logger.Errorf("Failed to purge key %s: %v", string(key), err)
				continue
			}
			stripe.DirMgr.DirTrie.Delete(key)
			purgedKeys = append(purgedKeys, keyStr)
		}
	}

	return purgedKeys
}

// FragmentKey generates a key for the Nth fragment.
// idx 0 is the base key.
// firstKey is the First Key (Earliest Key) of the object.
func FragmentKey(firstKey []byte, idx int) []byte {
	if idx == 0 {
		return firstKey
	}

	// Use robust hash combination (aligned with ATS MD5(key, idx))
	h := md5.New()
	h.Write(firstKey)

	idxBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(idxBytes, uint64(idx))
	h.Write(idxBytes)

	return h.Sum(nil)
}
