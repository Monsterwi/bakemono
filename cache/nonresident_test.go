package cache

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestNotResidentDoesNotClassifyCorruptionOrInvalidation(t *testing.T) {
	c := safetyCache(t)
	missing, err := c.OpenReadCommitted([]byte("absent"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = missing.LoadHTTPInfo()
	missing.Close()
	if !errors.Is(err, ErrNotResident) {
		t.Fatalf("missing fragment error=%v", err)
	}
	key := []byte("intact")
	w, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte("verified body")); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	stripe := c.KeyToStripe(key)
	if err = stripe.FlushAggBufferSync(); err != nil {
		t.Fatal(err)
	}
	_, _, dir := stripe.DirMgr.Get(key)
	value := []byte{0}
	offset := int64(dir.offset()) + DocHeaderSize
	if _, err = stripe.Fd.ReadAt(value, offset); err != nil {
		t.Fatal(err)
	}
	value[0] ^= 1
	if _, err = stripe.Fd.WriteAt(value, offset); err != nil {
		t.Fatal(err)
	}
	stripe.RamCache.Delete(key)
	corrupt, err := c.OpenReadCommitted(key)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(corrupt)
	corrupt.Close()
	if err == nil || errors.Is(err, ErrNotResident) {
		t.Fatalf("corruption classified as normal absence: %v", err)
	}
	old, err := c.OpenReadCommitted(key)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err = c.Remove(key); err != nil {
		t.Fatal(err)
	}
	_, err = old.Read(make([]byte, 1))
	if !errors.Is(err, ErrInvalidated) || errors.Is(err, ErrNotResident) {
		t.Fatalf("invalidation classified as absence: %v", err)
	}
}

// The ring can retain a directory entry after its old allocation is overwritten.
// A new reader must report absence, rather than corruption, for this normal case.
func TestNotResidentAfterRollingDiskOverRetainedDirectory(t *testing.T) {
	c := safetyCache(t)
	key := []byte("old-allocation")
	body := bytes.Repeat([]byte("x"), 1<<20)
	write := func(k []byte) {
		t.Helper()
		w, err := c.OpenWrite(k, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write(body); err != nil {
			t.Fatal(err)
		}
		if err = w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	write(key)
	stripe := c.KeyToStripe(key)
	_, _, d := stripe.DirMgr.Get(key)
	for n := 0; n < 64; n++ {
		write([]byte(fmt.Sprintf("new-%d", n)))
		stripe.mu.RLock()
		overwritten := d.phase() != stripe.Phase && int64(d.offset()) < stripe.WritePos
		stripe.mu.RUnlock()
		if !overwritten {
			continue
		}
		hit, _, _ := stripe.DirMgr.Get(key)
		if !hit {
			t.Fatal("expected retained old directory entry")
		}
		reader, err := c.OpenReadCommitted(key)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.ReadAll(reader)
		reader.Close()
		if !errors.Is(err, ErrNotResident) {
			t.Fatalf("normal ring overwrite error=%v", err)
		}
		return
	}
	t.Fatal("ring did not overwrite old allocation")
}

func TestNotResidentOnlyForNormalAllocationLoss(t *testing.T) {
	for _, name := range []string{"writing-overlap", "storage-error", "closed", "offset-before-start", "offset-after-end"} {
		t.Run(name, func(t *testing.T) {
			c := safetyCache(t)
			key := []byte("allocation")
			w, err := c.OpenWrite(key, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.Write([]byte("body")); err != nil {
				t.Fatal(err)
			}
			if err = w.Close(); err != nil {
				t.Fatal(err)
			}
			stripe := c.KeyToStripe(key)
			_, _, d := stripe.DirMgr.Get(key)
			failure := errors.New("injected storage failure")
			stripe.mu.Lock()
			savedDisk := stripe.disk
			switch name {
			case "writing-overlap":
				stripe.writingStart = int64(d.offset())
				stripe.writingEnd = int64(d.offset()) + int64(d.approxSize())
				stripe.writingGeneration = stripe.generation + 1
			case "storage-error":
				stripe.storageErr = failure
			case "closed":
				stripe.disk = nil
			case "offset-before-start":
				d.setOffset(uint64(stripe.Start - 1))
				stripe.DirMgr.Set(key, Offset(d.offset()), 4, d.phase())
			case "offset-after-end":
				d.setOffset(uint64(stripe.Skip + stripe.Len))
				stripe.DirMgr.Set(key, Offset(d.offset()), 4, d.phase())
			}
			stripe.mu.Unlock()
			r, openErr := c.OpenReadCommitted(key)
			err = openErr
			if err == nil {
				_, err = io.ReadAll(r)
				r.Close()
			}
			stripe.mu.Lock()
			stripe.disk = savedDisk
			stripe.storageErr = nil
			stripe.writingEnd = 0
			stripe.mu.Unlock()
			if name == "writing-overlap" {
				if !errors.Is(err, ErrNotResident) {
					t.Fatalf("overlap error=%v", err)
				}
			} else if err == nil || errors.Is(err, ErrNotResident) {
				t.Fatalf("hard error classified as absence: %v", err)
			}
			if name == "storage-error" && !errors.Is(err, failure) {
				t.Fatalf("lost storage error: %v", err)
			}
			if name == "closed" && !errors.Is(err, ErrCacheClosed) {
				t.Fatalf("lost closed error: %v", err)
			}
		})
	}
}

func TestCommittedReadReportsActiveWriterSeparately(t *testing.T) {
	c := safetyCache(t)
	key := []byte("in-progress")
	w, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	_, err = c.OpenReadCommitted(key)
	if !errors.Is(err, ErrWriteInProgress) || errors.Is(err, ErrNotResident) {
		t.Fatalf("active writer error=%v", err)
	}
}
