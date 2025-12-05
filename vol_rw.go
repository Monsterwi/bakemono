package bakemono

import (
	"hash/crc32"
)

func (v *Vol) getStripe(key []byte) *Stripe {
	h := crc32.ChecksumIEEE(key)
	idx := h % uint32(v.NumStripes)
	return v.Stripes[idx]
}

func (v *Vol) Set(key, value []byte) error {
	return v.getStripe(key).Set(key, value)
}

func (v *Vol) Get(key []byte) (bool, *CacheReader, error) {
	return v.getStripe(key).Get(key)
}

// Deprecated/Moved methods (kept if interfaces require, otherwise removed)
// checkSetRequest etc are now in Stripe.
