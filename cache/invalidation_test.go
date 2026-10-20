package cache

import (
	"context"
	"errors"
	"io"
	"path/filepath"
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
}

func TestPurgeRejectsClosedStripeBeforeChangingAnyStripe(t *testing.T) {
	c := safetyCache(t)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.PurgeSelector([]byte("/"), true); !errors.Is(err, ErrCacheClosed) {
		t.Fatalf("purge after close = %v", err)
	}
	if err := c.Remove([]byte("/x")); !errors.Is(err, ErrCacheClosed) {
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
