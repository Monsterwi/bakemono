package cache

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// CacheVC owns one read, write or removal operation and its acquired reference.
type CacheVC struct {
	done                       <-chan struct{}
	readCancel                 context.CancelFunc
	Stripe                     *Stripe
	Key                        []byte
	reading, writing, removing bool
	closed                     atomic.Bool
	closeOnce                  sync.Once
	closeErr                   error
	generation                 uint64
	entry                      *OpenDirEntry
	reader                     *CacheReader
	writer                     *CacheWriter
	Info                       *CacheHTTPInfo
}

func NewCacheVCRead(stripe *Stripe, key []byte) (*CacheVC, error) {
	ctx, cancel := context.WithCancel(context.Background())
	vc := &CacheVC{Stripe: stripe, Key: append([]byte(nil), key...), reading: true, done: ctx.Done(), readCancel: cancel}
	stripe.mu.Lock()
	if stripe.closing {
		stripe.mu.Unlock()
		cancel()
		return nil, ErrCacheClosed
	}
	vc.generation = stripe.OpenDir.Generation()
	vc.entry = stripe.OpenDir.Acquire(key)
	stripe.mu.Unlock()
	if vc.entry != nil {
		vc.reader = NewCacheReaderFromWriter(stripe, key, vc.entry.Writer)
	} else {
		vc.reader = NewCacheReader(stripe, key)
	}
	vc.reader.ctx = ctx
	return vc, nil
}

func NewCacheVCWrite(stripe *Stripe, key []byte) (*CacheVC, error) {
	c := &Cache{Stripes: []*Stripe{stripe}}
	token, err := c.BeginFill(key, key)
	if err != nil {
		return nil, err
	}
	return c.OpenWriteFill(context.Background(), token, 0)
}

func newCacheVCWrite(ctx context.Context, token *FillToken) (*CacheVC, error) {
	stripe, key := token.stripe, token.key
	if err := stripe.writeError(); err != nil {
		return nil, err
	}
	w := NewCacheWriter(stripe, key)
	if w.err != nil {
		w.cancel()
		return nil, w.err
	}
	w.cancel()
	w.ctx, w.cancel = context.WithCancel(ctx)
	w.generation, w.selector = token.generation, token.selector
	stripe.mu.Lock()
	defer stripe.mu.Unlock()
	if stripe.closing {
		w.cancel()
		return nil, ErrCacheClosed
	}
	if token.generation != stripe.OpenDir.Generation() {
		w.cancel()
		return nil, ErrInvalidated
	}
	if stripe.OpenDir.Get(key) != nil {
		w.cancel()
		return nil, fmt.Errorf("write conflict: another writer is active")
	}
	if err := stripe.reserveIdentityLocked(key, token.selector); err != nil {
		w.cancel()
		return nil, err
	}
	entry, _ := stripe.OpenDir.Add(key, w)
	_, _ = stripe.DirMgr.Delete(key)
	if stripe.RamCache != nil {
		stripe.RamCache.Delete(key)
	}
	return &CacheVC{Stripe: stripe, Key: append([]byte(nil), key...), writing: true, writer: w, entry: entry, generation: token.generation}, nil
}

func (vc *CacheVC) IsRWW() bool { return vc.reading && vc.reader != nil && vc.reader.writer != nil }

func (vc *CacheVC) WaitForHeaders(timeout time.Duration) error {
	if !vc.IsRWW() {
		return nil
	}
	select {
	case <-vc.entry.ReadySignal:
		vc.entry.mutex.RLock()
		err := vc.entry.Error
		vc.entry.mutex.RUnlock()
		if err != nil {
			return err
		}
		w := vc.reader.writer
		w.mutex.RLock()
		info, err := w.Info, w.err
		w.mutex.RUnlock()
		if err != nil {
			return err
		}
		if info == nil {
			return fmt.Errorf("writer finished without headers")
		}
		vc.reader.mutex.Lock()
		vc.Info, vc.reader.Info = info, info
		vc.reader.mutex.Unlock()
		return nil
	case <-vc.done:
		return fmt.Errorf("reader closed")
	case <-time.After(timeout):
		return fmt.Errorf("timeout waiting for headers")
	}
}

func (vc *CacheVC) Read(p []byte) (int, error) {
	if !vc.reading || vc.closed.Load() {
		return 0, fmt.Errorf("read operation not active or closed")
	}
	if vc.generation != vc.Stripe.OpenDir.Generation() {
		return 0, ErrInvalidated
	}
	return vc.reader.Read(p)
}

func (vc *CacheVC) Write(p []byte) (int, error) {
	if !vc.writing || vc.closed.Load() {
		return 0, fmt.Errorf("write operation not active or closed")
	}
	return vc.writer.Write(p)
}

func (vc *CacheVC) Close() error {
	vc.closeOnce.Do(func() {
		vc.closed.Store(true)
		if vc.readCancel != nil {
			vc.readCancel()
		}
		if vc.writing {
			vc.closeErr = vc.writer.Close()
			vc.writer.cancel()
		}
		vc.Stripe.OpenDir.Release(vc.Key, vc.entry)
	})
	return vc.closeErr
}

func (vc *CacheVC) Abort(err error) {
	// Cancel before waiting for Close: its flush may be blocked on storage I/O.
	if vc.writing {
		vc.writer.cancel()
	}
	vc.closeOnce.Do(func() {
		vc.closed.Store(true)
		if vc.readCancel != nil {
			vc.readCancel()
		}
		if vc.writing {
			vc.writer.Abort(err)
			vc.closeErr = vc.writer.Close()
		}
		vc.Stripe.OpenDir.Release(vc.Key, vc.entry)
	})
}

func (vc *CacheVC) SetHTTPInfo(info *CacheHTTPInfo) {
	if !vc.writing || vc.closed.Load() {
		return
	}
	vc.writer.mutex.Lock()
	defer vc.writer.mutex.Unlock()
	if !vc.writer.closed {
		vc.writer.Info, vc.Info = info, info
		vc.entry.signalReady()
	}
}

func (vc *CacheVC) LoadHTTPInfo() (*CacheHTTPInfo, error) {
	if vc.generation != vc.Stripe.OpenDir.Generation() {
		return nil, ErrInvalidated
	}
	if vc.reading {
		info, err := vc.reader.LoadHTTPInfo()
		if err == nil {
			vc.Info = info
		}
		return info, err
	}
	if vc.writing {
		vc.writer.mutex.RLock()
		defer vc.writer.mutex.RUnlock()
		return vc.writer.Info, nil
	}
	return nil, fmt.Errorf("no active operation")
}

func NewCacheVCRemove(stripe *Stripe, key []byte) (*CacheVC, error) {
	return &CacheVC{Stripe: stripe, Key: append([]byte(nil), key...), removing: true}, nil
}

func (vc *CacheVC) Remove() error {
	if !vc.removing || vc.closed.Load() {
		return fmt.Errorf("remove operation not active or closed")
	}
	return (&Cache{Stripes: []*Stripe{vc.Stripe}}).Remove(vc.Key)
}
