package bakemono

import (
	"github.com/maypok86/otter/v2"
	"github.com/maypok86/otter/v2/stats"
)

type RamCache struct {
	*otter.Cache[string, []byte]
}

func NewRamCache(capacity uint64) *RamCache {
	cache := otter.Must(&otter.Options[string, []byte]{
		MaximumWeight: capacity,
		Weigher: func(key string, value []byte) uint32 {
			return uint32(len(value))
		},
		StatsRecorder: stats.NewCounter(),
	})
	return &RamCache{Cache: cache}
}

func (r *RamCache) Get(key []byte) ([]byte, bool) {
	data, ok := r.GetIfPresent(string(key))
	return data, ok
}

func (r *RamCache) Put(key []byte, value []byte) error {
	r.Set(string(key), value)
	return nil
}

func (r *RamCache) Items() int {
	return r.EstimatedSize()
}

func (r *RamCache) Size() uint64 {
	return r.WeightedSize()
}

// func (r *RamCache) WeightedSize() uint64 {
// 	return r.WeightedSize()
// }
