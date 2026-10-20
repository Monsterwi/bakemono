package cache

import "sync"

const EvacuationBucketSize = 4 * 1024 * 1024

type EvacuationBlock struct {
	Key        [16]byte
	LogicalKey []byte
	Offset     int64
	Size       int
}

// Registrations are unique per disk offset, so repeated hits cannot grow memory.
type EvacuationTable struct {
	buckets map[int64]map[int64]EvacuationBlock
	mu      sync.Mutex
}

func NewEvacuationTable() *EvacuationTable {
	return &EvacuationTable{buckets: make(map[int64]map[int64]EvacuationBlock)}
}
func dirOffsetEvacBucket(offset int64) int64 { return offset / EvacuationBucketSize }
func (et *EvacuationTable) Register(key []byte, offset int64, size int) {
	et.mu.Lock()
	defer et.mu.Unlock()
	id := dirOffsetEvacBucket(offset)
	entries := et.buckets[id]
	if entries == nil {
		entries = make(map[int64]EvacuationBlock)
		et.buckets[id] = entries
	}
	digest := docKey(key)
	if old, ok := entries[offset]; ok && old.Key == digest {
		return
	}
	entries[offset] = EvacuationBlock{Key: digest, LogicalKey: append([]byte(nil), key...), Offset: offset, Size: size}
}
func (et *EvacuationTable) GetCandidatesInRange(start, end int64) []EvacuationBlock {
	et.mu.Lock()
	defer et.mu.Unlock()
	var result []EvacuationBlock
	for id := dirOffsetEvacBucket(start); id <= dirOffsetEvacBucket(end-1); id++ {
		entries := et.buckets[id]
		for offset, e := range entries {
			if offset < end {
				delete(entries, offset)
				if offset >= start {
					result = append(result, e)
				}
			}
		}
		if len(entries) == 0 {
			delete(et.buckets, id)
		}
	}
	return result
}
