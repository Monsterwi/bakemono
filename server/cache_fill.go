package server

import (
	"context"
	"errors"
	"time"

	"github.com/Monsterwi/razor/cache"
)

// cacheFill bounds cache-only work. A slow or oversized cache fill is abandoned
// without canceling an otherwise healthy origin stream.
type cacheFill struct {
	release        func()
	vc             *cache.CacheVC
	cancel         context.CancelFunc
	timeout        time.Duration
	limit, written int64
	stats          *counters
}

func (f *cacheFill) abort(err error) {
	if f.vc != nil {
		f.cancel()
		f.vc.Abort(err)
		f.vc = nil
		if f.release != nil {
			f.release()
			f.release = nil
		}
	}
}
func (f *cacheFill) write(p []byte) {
	if f == nil || f.vc == nil {
		return
	}
	f.written += int64(len(p))
	if f.written > f.limit {
		f.stats.cacheSkipped.Add(1)
		f.abort(errors.New("cache object limit"))
		return
	}
	timer := time.AfterFunc(f.timeout, f.cancel)
	_, err := f.vc.Write(p)
	timer.Stop()
	if err != nil {
		f.stats.cacheErrors.Add(1)
		f.abort(err)
	}
}
func (f *cacheFill) commit() {
	if f == nil || f.vc == nil {
		return
	}
	timer := time.AfterFunc(f.timeout, f.cancel)
	err := f.vc.Close()
	timer.Stop()
	if err != nil {
		f.stats.cacheErrors.Add(1)
	}
	f.cancel()
	f.vc = nil
	if f.release != nil {
		f.release()
		f.release = nil
	}
}
