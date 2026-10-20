package cache

import (
	"context"
	"errors"
	"sort"
)

var ErrInvalidated = errors.New("cache operation invalidated")

// FillToken must be captured before fetching origin bytes. A purge conservatively
// fences all unfinished fills on the affected stripe, without retaining key history.
type FillToken struct {
	stripe        *Stripe
	key, selector []byte
	generation    uint64
	stripes       []*Stripe
	generations   []uint64
}

func (c *Cache) BeginFill(key, selector []byte) (*FillToken, error) {
	if err := validateIdentity(key, selector); err != nil {
		return nil, err
	}
	if c.KeyToStripe(key) == nil {
		return nil, errors.New("no stripe found")
	}
	for _, stripe := range c.Stripes {
		stripe.mu.Lock()
	}
	defer func() {
		for i := len(c.Stripes) - 1; i >= 0; i-- {
			c.Stripes[i].mu.Unlock()
		}
	}()
	token := &FillToken{key: append([]byte(nil), key...), selector: append([]byte(nil), selector...), stripes: append([]*Stripe(nil), c.Stripes...), generations: make([]uint64, len(c.Stripes))}
	for i, stripe := range c.Stripes {
		if stripe.closing {
			return nil, ErrCacheClosed
		}
		token.generations[i] = stripe.OpenDir.Generation()
	}
	return token, nil
}

func (c *Cache) OpenWriteFill(ctx context.Context, token *FillToken, opts int) (*CacheVC, error) {
	if token == nil {
		return nil, errors.New("invalid cache fill token")
	}
	return c.OpenWriteFillKey(ctx, token, token.key, opts)
}

// OpenWriteFillKey binds a representation chosen after origin headers, preserving
// the generation captured before the origin request on its destination stripe.
func (c *Cache) OpenWriteFillKey(ctx context.Context, token *FillToken, key []byte, opts int) (*CacheVC, error) {
	if token == nil || len(token.stripes) != len(c.Stripes) {
		return nil, errors.New("invalid cache fill token")
	}
	if err := validateIdentity(key, token.selector); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	bound := *token
	bound.key = append([]byte(nil), key...)
	bound.stripe = c.KeyToStripe(key)
	for i, stripe := range c.Stripes {
		if token.stripes[i] != stripe {
			return nil, errors.New("foreign cache fill token")
		}
		if stripe == bound.stripe {
			bound.generation = token.generations[i]
		}
	}
	return newCacheVCWrite(ctx, &bound)
}

// KeysForSelector returns all representations across all stripes.
func (c *Cache) KeysForSelector(selector []byte) []string {
	var keys []string
	for _, stripe := range c.Stripes {
		keys = append(keys, stripe.DirMgr.DirTrie.KeysForSelector(selector)...)
	}
	sort.Strings(keys)
	return keys
}

// PurgeSelector invalidates exact URL selectors or explicit selector prefixes.
func (c *Cache) PurgeSelector(selector []byte, prefix bool) ([]string, error) {
	if len(selector) == 0 {
		return nil, errors.New("empty purge selector")
	}
	// Lock in configured order so close cannot publish a clean checkpoint midway.
	for _, stripe := range c.Stripes {
		stripe.mu.Lock()
	}
	unlock := func() {
		for i := len(c.Stripes) - 1; i >= 0; i-- {
			c.Stripes[i].mu.Unlock()
		}
	}
	for _, stripe := range c.Stripes {
		if stripe.closing {
			unlock()
			return nil, ErrCacheClosed
		}
	}
	var purged []string
	var writers []*CacheWriter
	for _, stripe := range c.Stripes {
		stripe.OpenDir.generation.Add(1)
		keys := stripe.DirMgr.DirTrie.KeysForSelector(selector)
		if prefix {
			keys = stripe.DirMgr.DirTrie.Get(selector)
		}
		for _, key := range keys {
			if writer := stripe.removeLocked([]byte(key)); writer != nil {
				writers = append(writers, writer)
			}
			purged = append(purged, key)
		}
	}
	unlock()
	for _, writer := range writers {
		writer.Abort(ErrInvalidated)
	}
	sort.Strings(purged)
	return purged, nil
}

// removeLocked runs under the same barrier as directory and RAM publication.
func (s *Stripe) removeLocked(key []byte) *CacheWriter {
	entry := s.OpenDir.detach(key)
	// A missing head still needs its RAM copy and any active reservation removed.
	s.DirMgr.DirTrie.Delete(key)
	for idx := 0; idx < int(s.DirMgr.FragmentsNum); idx++ {
		fragment := FragmentKey(key, idx)
		hit, _, _ := s.DirMgr.Get(fragment)
		if s.RamCache != nil {
			s.RamCache.Delete(fragment)
		}
		if !hit {
			break
		}
		_, _ = s.DirMgr.Delete(fragment)
	}
	if entry != nil {
		return entry.Writer
	}
	return nil
}

// reserveIdentityLocked bounds retained key history and keeps active reservations.
func (s *Stripe) reserveIdentityLocked(key, selector []byte) error {
	err := s.DirMgr.DirTrie.SetIdentity(key, selector)
	if !errors.Is(err, ErrIndexFull) {
		return err
	}
	for _, entry := range s.DirMgr.DirTrie.Snapshot() {
		k := []byte(entry.Key)
		if s.OpenDir.Get(k) != nil {
			continue
		}
		hit, _, dir := s.DirMgr.Get(k)
		if !hit || !s.dirValid(dir) {
			s.DirMgr.DirTrie.Delete(k)
		}
	}
	return s.DirMgr.DirTrie.SetIdentity(key, selector)
}

// abortOpenWriters is called after Close shuts admission, outside Stripe.mu.
func (s *Stripe) abortOpenWriters(err error) {
	s.mu.Lock()
	s.OpenDir.generation.Add(1)
	s.OpenDir.mutex.RLock()
	keys := make([]string, 0, len(s.OpenDir.entries))
	for key := range s.OpenDir.entries {
		keys = append(keys, key)
	}
	s.OpenDir.mutex.RUnlock()
	writers := make([]*CacheWriter, 0, len(keys))
	for _, key := range keys {
		if entry := s.OpenDir.detach([]byte(key)); entry != nil {
			writers = append(writers, entry.Writer)
		}
	}
	s.mu.Unlock()
	for _, writer := range writers {
		writer.Abort(err)
	}
}
