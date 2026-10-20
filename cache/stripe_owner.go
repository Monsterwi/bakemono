package cache

import (
	"fmt"
	"os"
	"sync"
	"syscall"
)

type stripeInode struct{ dev, ino uint64 }
type stripeOwner struct {
	fd      int
	stripes map[*Stripe]struct{}
}

var stripeOwners = struct {
	sync.Mutex
	files map[stripeInode]*stripeOwner
}{files: make(map[stripeInode]*stripeOwner)}

// One process owns an inode; that process may open disjoint split spans.
func (s *Stripe) acquireOwnership(f *os.File) error {
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	raw, ok := stat.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("cache file identity unavailable")
	}
	key := stripeInode{uint64(raw.Dev), uint64(raw.Ino)}
	stripeOwners.Lock()
	defer stripeOwners.Unlock()
	owner := stripeOwners.files[key]
	if owner != nil {
		for other := range owner.stripes {
			if s.Skip < other.Skip+other.Len && other.Skip < s.Skip+s.Len {
				return fmt.Errorf("overlapping cache stripe already open")
			}
		}
	} else {
		fd, err := syscall.Dup(int(f.Fd()))
		if err != nil {
			return err
		}
		syscall.CloseOnExec(fd)
		if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			_ = syscall.Close(fd)
			return fmt.Errorf("cache file already owned: %w", err)
		}
		owner = &stripeOwner{fd: fd, stripes: make(map[*Stripe]struct{})}
		stripeOwners.files[key] = owner
	}
	owner.stripes[s] = struct{}{}
	s.releaseOwnership = func() {
		stripeOwners.Lock()
		defer stripeOwners.Unlock()
		delete(owner.stripes, s)
		if len(owner.stripes) == 0 {
			_ = syscall.Close(owner.fd)
			delete(stripeOwners.files, key)
		}
	}
	return nil
}
