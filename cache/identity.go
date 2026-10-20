package cache

import "crypto/sha256"

const docIdentityVersion = 3

// docKey hashes the entire logical identity into the fixed-width on-disk key.
func docKey(key []byte) [16]byte {
	digest := sha256.Sum256(key)
	return [16]byte(digest[:16])
}
