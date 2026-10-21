package cache

import (
	"fmt"

	"github.com/maypok86/otter/v2"
	"github.com/maypok86/otter/v2/stats"
)

const maxRAMKeyBytes = 64 << 10

// RamCache retains immutable serialized fragments; a miss always permits disk fallback.
// The selected policy owns no HTTP or persistent-cache invalidation state.
type RamCache struct {
	otter     *otter.Cache[string, []byte]
	fifo      *s3FIFO
	capacity  uint64
	algorithm string
}

type RAMStats struct {
	stats.Stats
	Algorithm                                                 string
	CapacityBytes, ResidentBytes, ChargedBytes                uint64
	ResidentEntries, GhostEntries, SmallEntries, MainEntries  int
	GhostBytes, EstimatedMetadataBytes, Promotions, GhostHits uint64
}

func NewRamCache(capacity uint64) *RamCache {
	c, err := NewRamCacheWithAlgorithm(capacity, "otter")
	if err != nil {
		panic(err)
	}
	return c
}

func NewRamCacheWithAlgorithm(capacity uint64, algorithm string) (*RamCache, error) {
	if algorithm != "otter" && algorithm != "s3-fifo" {
		return nil, fmt.Errorf("unknown RAM cache algorithm %q", algorithm)
	}
	if capacity > 1<<40 {
		return nil, fmt.Errorf("RAM cache capacity exceeds 1 TiB")
	}
	r := &RamCache{capacity: capacity, algorithm: algorithm}
	if capacity == 0 {
		return r, nil
	}
	if algorithm == "s3-fifo" {
		r.fifo = newS3FIFO(capacity)
		return r, nil
	}
	r.otter = otter.Must(&otter.Options[string, []byte]{MaximumWeight: capacity, Weigher: func(_ string, value []byte) uint32 { return uint32(max(1, len(value))) }, StatsRecorder: stats.NewCounter()})
	return r, nil
}

func (r *RamCache) Get(key []byte) ([]byte, bool) {
	if r.fifo != nil {
		return r.fifo.get(string(key))
	}
	if r.otter == nil {
		return nil, false
	}
	return r.otter.GetIfPresent(string(key))
}
func (r *RamCache) Put(key, value []byte) error {
	// Rejecting an overwrite must also discard its previous mapping.
	if r.capacity == 0 || uint64(len(value)) > r.capacity || uint64(len(value)) > uint64(^uint32(0)) || len(key) > maxRAMKeyBytes {
		r.Delete(key)
		return nil
	}
	if r.fifo != nil {
		r.fifo.put(string(key), value)
	} else {
		r.otter.Set(string(key), value)
	}
	return nil
}
func (r *RamCache) Delete(key []byte) {
	if r.fifo != nil {
		r.fifo.delete(string(key))
	} else if r.otter != nil {
		r.otter.Invalidate(string(key))
	}
}
func (r *RamCache) Items() int {
	if r.fifo != nil {
		r.fifo.mu.Lock()
		defer r.fifo.mu.Unlock()
		return r.fifo.queues[fifoSmall].Len() + r.fifo.queues[fifoMain].Len()
	}
	if r.otter == nil {
		return 0
	}
	return r.otter.EstimatedSize()
}

// Size reports charged resident bytes: empty values reserve one byte in both policies.
func (r *RamCache) Size() uint64 {
	if r.fifo != nil {
		r.fifo.mu.Lock()
		defer r.fifo.mu.Unlock()
		return r.fifo.smallBytes + r.fifo.mainBytes
	}
	if r.otter == nil {
		return 0
	}
	return r.otter.WeightedSize()
}
func (r *RamCache) CleanUp() {
	if r.otter != nil {
		r.otter.CleanUp()
	}
}

// Stats is a sampling API, not a request-path operation. Otter's supported iterator
// is weakly consistent during concurrent mutation; neither estimate is an RSS limit.
func (r *RamCache) Stats() RAMStats {
	st := RAMStats{Algorithm: r.algorithm, CapacityBytes: r.capacity}
	if r.fifo != nil {
		st = r.fifo.snapshot()
		st.Algorithm = r.algorithm
		st.CapacityBytes = r.capacity
		return st
	}
	if r.otter == nil {
		return st
	}
	st.Stats = r.otter.Stats()
	st.ChargedBytes = r.otter.WeightedSize()
	for key, value := range r.otter.All() {
		st.ResidentEntries++
		st.ResidentBytes += uint64(len(value))
		st.EstimatedMetadataBytes += uint64(128 + len(key))
	}
	return st
}
