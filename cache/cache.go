package cache

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
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

// FragmentKey generates a key for the Nth fragment.
// idx 0 is the base key.
func FragmentKey(base []byte, idx int) []byte {
	if idx == 0 {
		return base
	}
	// Create a new key based on base + idx
	// In ATS: key[0] might be modified.
	// Here we append index to hash or modify last bytes.
	// Simple impl: use hashing or modify.
	// Let's modify the first 4 bytes (uint32) by adding idx.
	// Assuming key is at least 16 bytes.

	fk := make([]byte, len(base))
	copy(fk, base)

	if len(fk) >= 4 {
		// Mix idx into the key
		// Just adding to first uint32 is simple deterministic way
		val := binary.BigEndian.Uint32(fk[:4])
		binary.BigEndian.PutUint32(fk[:4], val+uint32(idx))
	}
	return fk
}
