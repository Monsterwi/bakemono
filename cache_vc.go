package bakemono

import (
	"crypto/md5"
)

// CacheVC (Cache Virtual Connection) acts as the context for a cache operation (read/write).
// It holds the state, keys, and reference to the Stripe.
// Ref: trafficserver/src/iocore/cache/CacheVC.h
type CacheVC struct {
	stripe *Stripe
	dir    Dir

	// Keys
	key         []byte // The key for the current operation
	firstKey    []byte // The first key (metadata key)
	earliestKey []byte // The earliest key (first data fragment key)

	// Offset state
	offset int64 // logical offset

	// Error state
	err error
}

func NewCacheVC(s *Stripe, key []byte) *CacheVC {
	vc := &CacheVC{
		stripe: s,
		key:    key,
	}
	// Initialize keys
	// Default: firstKey is the provided key
	vc.firstKey = make([]byte, len(key))
	copy(vc.firstKey, key)

	// Default: earliestKey is hashed from key (to allow separation for multi-frag)
	h := md5.Sum(key)
	vc.earliestKey = h[:]

	return vc
}
