package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestDerivedWriterRetainsOriginalPolicyFence(t *testing.T) {
	c := safetyCache(t)
	root, _ := c.BeginFill([]byte("/range"), []byte("/range"))
	derived, err := c.DeriveFill(root, []byte("/range\nrange:block:one"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := c.OpenWriteFill(context.Background(), derived, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte("old block")); err != nil {
		t.Fatal(err)
	}
	if err = c.InvalidateFill(root); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); !errors.Is(err, ErrInvalidated) {
		t.Fatalf("old writer committed after policy change: %v", err)
	}
	if _, err = c.DeriveFill(root, []byte("/range\nrange:block:two")); !errors.Is(err, ErrInvalidated) {
		t.Fatalf("old parent derived new fill: %v", err)
	}
}

func TestDerivedSelectorsExceedVariantLimitAndPurgeOnlyExactFamily(t *testing.T) {
	c := safetyCache(t)
	root, _ := c.BeginFill([]byte("/range"), []byte("/range"))
	for i := 0; i < 65; i++ {
		key := []byte(fmt.Sprintf("/range\nrange:block:%d", i))
		token, err := c.DeriveFill(root, key)
		if err != nil {
			t.Fatal(i, err)
		}
		w, err := c.OpenWriteFill(context.Background(), token, 0)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("verified"))
		if err = w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	w, _ := c.OpenWrite([]byte("/range-adjacent"), 0)
	w.Write([]byte("keep"))
	w.Close()
	if _, err := c.BeginFill([]byte("/range"), []byte("/range")); err != nil {
		t.Fatal(err)
	}
	removed, err := c.PurgeSelector([]byte("/range"), false)
	if err != nil || len(removed) != 65 {
		t.Fatalf("purged=%d err=%v", len(removed), err)
	}
	r, _ := c.OpenReadCommitted([]byte("/range-adjacent"))
	defer r.Close()
	body, err := io.ReadAll(r)
	if err != nil || string(body) != "keep" {
		t.Fatalf("adjacent=%q %v", body, err)
	}
}

func TestUnsafePolicyRemovesOnlyCapturedRangeManifest(t *testing.T) {
	c := safetyCache(t)
	write := func(key, body string) {
		root, _ := c.BeginFill([]byte("/item"), []byte("/item"))
		tok, err := c.DeriveFill(root, []byte(key))
		if err != nil {
			t.Fatal(err)
		}
		w, err := c.OpenWriteFill(context.Background(), tok, 0)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
		if err = w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	const key = "/item\nrange:manifest:v2:1048576:origin"
	write(key, "old")
	old, _ := c.BeginFill([]byte("/item"), []byte("/item"))
	if err := c.InvalidateFill(old); err != nil {
		t.Fatal(err)
	}
	r, _ := c.OpenReadCommitted([]byte(key))
	_, err := io.ReadAll(r)
	r.Close()
	if err == nil {
		t.Fatal("unsafe ordinary policy left manifest reachable")
	}
	write(key, "first")
	stale, _ := c.BeginFill([]byte("/item"), []byte("/item"))
	write(key, "newer")
	if err := c.InvalidateFill(stale); err != nil {
		t.Fatal(err)
	}
	r, _ = c.OpenReadCommitted([]byte(key))
	body, err := io.ReadAll(r)
	r.Close()
	if err != nil || string(body) != "newer" {
		t.Fatalf("newer manifest destroyed: %q %v", body, err)
	}
}
