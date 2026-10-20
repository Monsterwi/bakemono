package cache

import (
	"sync"
)

const (
	// EvacuationBucketSize is the size of the region covered by one evacuation bucket.
	// ATS uses ~2MB or similar. Let's align with AggBufferSize for simplicity or larger.
	EvacuationBucketSize = 4 * 1024 * 1024 // 4MB
)

// EvacuationBlock represents an item that needs to be evacuated.
// Aligned with ATS EvacuationBlock structure.
type EvacuationBlock struct {
	Key        [16]byte
	LogicalKey []byte
	Offset     int64
	Size       int
}

// EvacuationTable manages evacuation buckets.
// It maps disk regions to lists of entries that need evacuation.
// Aligned with ATS PreservationTable (we use EvacuationTable for clarity).
type EvacuationTable struct {
	// buckets is a map of BucketID -> []EvacuationBlock
	// BucketID = dir_offset_evac_bucket(Offset)
	buckets map[int64][]EvacuationBlock
	mu      sync.RWMutex
}

func NewEvacuationTable() *EvacuationTable {
	return &EvacuationTable{
		buckets: make(map[int64][]EvacuationBlock),
	}
}

// dirOffsetEvacBucket calculates the evacuation bucket ID for a given offset.
// Aligned with ATS dir_offset_evac_bucket() macro.
func dirOffsetEvacBucket(offset int64) int64 {
	return offset / EvacuationBucketSize
}

// Register adds an entry to the evacuation table.
// This is typically called by Readers when they access an object in the evacuation window.
func (et *EvacuationTable) Register(key []byte, offset int64, size int) {
	et.mu.Lock()
	defer et.mu.Unlock()

	bucketID := dirOffsetEvacBucket(offset)

	// Check for duplicates? For performance, maybe just append.
	// Or we can simple check the last one.
	entries := et.buckets[bucketID]

	// Basic dedup (optional, depends on frequency)
	// For now, blind append.

	k := docKey(key)

	et.buckets[bucketID] = append(entries, EvacuationBlock{
		Key:        k,
		LogicalKey: append([]byte(nil), key...),
		Offset:     offset,
		Size:       size,
	})
}

// GetCandidatesInRange returns all entries in the range [start, end).
// It clears the accessed buckets as they are processed (assuming linear scan).
// This is used by evac_range() logic, aligned with ATS evac_range() behavior.
func (et *EvacuationTable) GetCandidatesInRange(start, end int64) []EvacuationBlock {
	et.mu.Lock()
	defer et.mu.Unlock()

	var candidates []EvacuationBlock

	startBucket := dirOffsetEvacBucket(start)
	endBucket := dirOffsetEvacBucket(end - 1)

	for i := startBucket; i <= endBucket; i++ {
		if entries, ok := et.buckets[i]; ok {
			// Filter entries that are strictly within range
			// Since bucket boundary aligns with size, most will be, but let's be precise.
			// Also, we want to remove them from the map to save memory.

			// For simplicity in this implementation:
			// We take the whole bucket and return valid ones.
			// And we DELETE the bucket.
			// Note: If 'end' is in the middle of a bucket, we might delete entries that are not yet evacuated?
			// Correct Logic: Evacuation usually happens by Bucket granularity or we re-insert unprocessed ones.
			// But here 'collectEvacuationData' is called with precise byte ranges from Flush.

			// Strategy:
			// 1. Extract all from bucket.
			// 2. Append in-range ones to candidates.
			// 3. Keep out-of-range ones (future) in the bucket.
			// 4. Discard already-passed ones (past).

			var remaining []EvacuationBlock
			for _, e := range entries {
				if e.Offset >= start && e.Offset < end {
					candidates = append(candidates, e)
				} else if e.Offset >= end {
					remaining = append(remaining, e)
				}
				// e.Offset < start are implicitly dropped (already passed)
			}

			if len(remaining) > 0 {
				et.buckets[i] = remaining
			} else {
				delete(et.buckets, i)
			}
		}
	}

	return candidates
}
