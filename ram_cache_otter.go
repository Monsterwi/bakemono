package bakemono

import (
	"github.com/maypok86/otter/v2"
	"github.com/maypok86/otter/v2/stats"
)

type RamCacheOtter struct {
	*otter.Cache[string, []byte]
}

func NewRamCacheOtter(entries uint64) *RamCacheOtter {
	cache := otter.Must(&otter.Options[string, []byte]{
		MaximumSize:   int(entries),
		StatsRecorder: stats.NewCounter(),
	})
	return &RamCacheOtter{Cache: cache}
}

func (r *RamCacheOtter) Get(key []byte) ([]byte, error) {
	data, ok := r.GetIfPresent(string(key))
	if !ok {
		return nil, nil
	}
	return data, nil
}

func (r *RamCacheOtter) Put(key []byte, value []byte) error {
	r.Set(string(key), value)
	return nil
}

func (r *RamCacheOtter) Size() int64 {
	return int64(r.EstimatedSize())
}
