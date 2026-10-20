package cache

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestPurgeFencesOldWriterBeforeNewPublication(t *testing.T) {
	c := safetyCache(t)
	key := []byte("/old-writer")
	old, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	old.Write([]byte("old"))
	reader, _ := c.OpenRead(key)
	defer reader.Close()
	waiting := make(chan error, 1)
	go func() { waiting <- reader.WaitForHeaders(time.Second) }()
	if err := c.Remove(key); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waiting:
		if err == nil {
			t.Fatal("invalidated waiter succeeded")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("PURGE left header waiter blocked")
	}
	newer, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	newer.Write([]byte("new"))
	if err = newer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = old.Close(); err == nil {
		t.Fatal("invalidated writer committed")
	}
	r, _ := c.OpenReadCommitted(key)
	defer r.Close()
	body, err := io.ReadAll(r)
	if err != nil || string(body) != "new" {
		t.Fatalf("replacement = %q, %v", body, err)
	}
	path := c.Stripes[0].Path
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openInvalidationCache(t, path)
	defer reopened.Close()
	assertInvalidationBody(t, reopened, string(key), "new")
}

func TestPurgeRejectsClosedStripeBeforeChangingAnyStripe(t *testing.T) {
	dir := t.TempDir()
	c, err := NewCache(2, []string{filepath.Join(dir, "one"), filepath.Join(dir, "two")}, []int64{32 << 20, 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Init(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	key := []byte("still-open")
	writer, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	writer.Write([]byte("keep"))
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	other := c.Stripes[0]
	if other == c.KeyToStripe(key) {
		other = c.Stripes[1]
	}
	if err = other.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = c.PurgeSelector(key, false); !errors.Is(err, ErrCacheClosed) {
		t.Fatalf("partly closed purge = %v", err)
	}
	assertInvalidationBody(t, c, string(key), "keep")
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.Remove(key); !errors.Is(err, ErrCacheClosed) {
		t.Fatalf("remove after close = %v", err)
	}
}

func TestFillTokenCanBindAnotherStripeWithoutLosingPurgeFence(t *testing.T) {
	c, err := NewCache(2, []string{filepath.Join(t.TempDir(), "one"), filepath.Join(t.TempDir(), "two")}, []int64{32 << 20, 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Init(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	selector := []byte("https://host/folder/object")
	base := []byte("base")
	variant := []byte("variant-a")
	if c.KeyToStripe(base) == c.KeyToStripe(variant) {
		variant = []byte("variant-b")
	}
	if c.KeyToStripe(base) == c.KeyToStripe(variant) {
		t.Fatal("fixture keys must select different stripes")
	}
	old, err := c.BeginFill(base, selector)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.PurgeSelector([]byte("https://host/folder/"), true); err != nil {
		t.Fatal(err)
	}
	if w, err := c.OpenWriteFillKey(context.Background(), old, variant, 0); !errors.Is(err, ErrInvalidated) {
		if w != nil {
			w.Abort(io.EOF)
		}
		t.Fatalf("old late-bound fill = %v", err)
	}
	fresh, _ := c.BeginFill(base, selector)
	for _, key := range [][]byte{base, variant} {
		w, err := c.OpenWriteFillKey(context.Background(), fresh, key, 0)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("new"))
		if err = w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{c.Stripes[0].Path, c.Stripes[1].Path}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = NewCache(2, paths, []int64{32 << 20, 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Init(); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if keys := c.KeysForSelector(selector); len(keys) != 2 {
		t.Fatalf("selector representations = %v", keys)
	}
	if keys, err := c.PurgeSelector(selector, false); err != nil || len(keys) != 2 {
		t.Fatalf("purge variants = %v, %v", keys, err)
	}
	for _, key := range [][]byte{base, variant} {
		r, _ := c.OpenReadCommitted(key)
		_, err := io.ReadAll(r)
		r.Close()
		if err == nil {
			t.Fatal("purged variant remained readable")
		}
	}
}

func TestReaderCloseWakesItsHeaderWaiterWithoutReleasingWriter(t *testing.T) {
	c := safetyCache(t)
	key := []byte("reader-close")
	writer, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Abort(io.EOF)
	reader, err := c.OpenRead(key)
	if err != nil {
		t.Fatal(err)
	}
	waiting := make(chan error, 1)
	go func() { waiting <- reader.WaitForHeaders(time.Second) }()
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-waiting:
		if err == nil {
			t.Fatal("closed waiter succeeded")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("reader Close left header wait blocked")
	}
	if next, err := c.OpenWrite(key, 0); err == nil {
		next.Abort(io.EOF)
		t.Fatal("reader Close released writer ownership")
	}
}

func TestConcurrentCloseAbortRetainsWriterAndReleasesEachReaderOnce(t *testing.T) {
	c := safetyCache(t)
	key := []byte("reference-ownership")
	for iteration := 0; iteration < 20; iteration++ {
		writer, err := c.OpenWrite(key, 0)
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for n := 0; n < 8; n++ {
			reader, err := c.OpenRead(key)
			if err != nil {
				t.Fatal(err)
			}
			wg.Add(2)
			go func() { defer wg.Done(); reader.Close() }()
			go func() { defer wg.Done(); reader.Abort(io.EOF) }()
		}
		wg.Wait()
		if conflict, err := c.OpenWrite(key, 0); err == nil {
			conflict.Abort(io.EOF)
			t.Fatal("reader releases removed active writer")
		}
		wg.Add(2)
		go func() { defer wg.Done(); writer.Close() }()
		go func() { defer wg.Done(); writer.Abort(io.EOF) }()
		wg.Wait()
	}
}

func TestExactPurgeDoesNotDeleteDirectoryTagCollision(t *testing.T) {
	c := safetyCache(t)
	a, b := []byte("0123456789abcdef/72"), []byte("0123456789abcdef/920")
	w, err := c.OpenWrite(a, 0)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("keep collision neighbor"))
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.Remove(b); err != nil {
		t.Fatal(err)
	}
	r, err := c.OpenReadCommitted(a)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil || string(got) != "keep collision neighbor" {
		t.Fatalf("exact purge deleted neighbor: %q,%v", got, err)
	}
}

func TestReaderCloseCancelsBlockedBodyReadWithoutAbortingWriter(t *testing.T) {
	c := safetyCache(t)
	key := []byte("cancel-reader-body")
	writer, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Abort(io.EOF)
	reader, err := c.OpenRead(key)
	if err != nil {
		t.Fatal(err)
	}
	read := make(chan error, 1)
	go func() { _, err := reader.Read(make([]byte, 1)); read <- err }()
	select {
	case err := <-read:
		t.Fatalf("read did not wait for body: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	reader.Close()
	select {
	case err := <-read:
		if err == nil {
			t.Fatal("closed reader returned success")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("reader Close did not cancel blocked read")
	}
	if _, err := writer.Write([]byte("writer remains usable")); err != nil {
		t.Fatalf("reader Close aborted writer: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentReadWritePurgeCannotLeaveStalePublisher(t *testing.T) {
	c := safetyCache(t)
	key := []byte("/concurrent/object")
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			w, err := c.OpenWrite(key, 0)
			if err != nil {
				continue
			}
			w.Write([]byte("racing"))
			w.Close()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			r, err := c.OpenReadCommitted(key)
			if err != nil {
				continue
			}
			io.ReadAll(r)
			r.Close()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			c.PurgeSelector([]byte("/concurrent/"), true)
		}
	}()
	wg.Wait()
	w, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("final"))
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	assertInvalidationBody(t, c, string(key), "final")
}

func TestFailedCacheInitReleasesEarlierStripeOwnership(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opened")
	c, err := NewCache(2, []string{path, dir}, []int64{32 << 20, 32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Init(); err == nil {
		c.Close()
		t.Fatal("accepted directory as second stripe file")
	}
	reopened := openInvalidationCache(t, path)
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
}
