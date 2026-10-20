package cache

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

func TestConditionalFillRejectsChangedFamilyButAllowsUnrelatedFill(t *testing.T) {
	c := safetyCache(t)
	selector := []byte("url")
	old, _ := c.BeginFill(selector, selector)
	unrelated, _ := c.BeginFill([]byte("other"), []byte("other"))
	first, err := c.OpenWriteFillKeyReplacing(context.Background(), old, []byte("variant"), nil)
	if err != nil {
		t.Fatal(err)
	}
	first.Write([]byte("new"))
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := c.OpenWriteFillKeyReplacing(context.Background(), unrelated, []byte("other"), nil)
	if err != nil {
		t.Fatal("unrelated fill fenced:", err)
	}
	other.Write([]byte("other"))
	other.Close()
	if w, err := c.OpenWriteFillKeyReplacing(context.Background(), old, []byte("stale"), []string{"variant"}); !errors.Is(err, ErrInvalidated) {
		if w != nil {
			w.Abort(io.EOF)
		}
		t.Fatalf("stale replacement=%v", err)
	}
	assertInvalidationBody(t, c, "variant", "new")
	fresh, _ := c.BeginFill(selector, selector)
	replacement, err := c.OpenWriteFillKeyReplacing(context.Background(), fresh, []byte("v2"), []string{"variant"})
	if err != nil {
		t.Fatal(err)
	}
	replacement.Write([]byte("replacement"))
	replacement.Close()
	if keys := c.KeysForSelector(selector); len(keys) != 1 || keys[0] != "v2" {
		t.Fatalf("family=%v", keys)
	}
}

func TestMetadataRefreshPreservesFragmentedBodyAndRejectsPurge(t *testing.T) {
	c := safetyCache(t)
	key := []byte("refresh")
	body := bytes.Repeat([]byte("body"), TargetFragmentSize)
	w, _ := c.OpenWrite(key, 0)
	info := NewCacheHTTPInfo()
	info.Status = 200
	info.ResponseHeaders.Set("ETag", `"v1"`)
	w.SetHTTPInfo(info)
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	token, _ := c.BeginFill(key, key)
	r, _ := c.OpenReadCommitted(key)
	defer r.Close()
	old, err := r.LoadHTTPInfo()
	if err != nil {
		t.Fatal(err)
	}
	updated := *old
	updated.ResponseHeaders = old.ResponseHeaders.Clone()
	updated.ResponseHeaders.Set("X-Updated", "yes")
	if err = c.UpdateHTTPInfo(context.Background(), token, r, &updated); err != nil {
		t.Fatal(err)
	}
	fresh, _ := c.OpenReadCommitted(key)
	defer fresh.Close()
	metadata, err := fresh.LoadHTTPInfo()
	if err != nil || metadata.ResponseHeaders.Get("X-Updated") != "yes" {
		t.Fatalf("metadata=%v err=%v", metadata, err)
	}
	got, err := io.ReadAll(fresh)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("body len=%d err=%v", len(got), err)
	}
	token, _ = c.BeginFill(key, key)
	r2, _ := c.OpenReadCommitted(key)
	defer r2.Close()
	r2.LoadHTTPInfo()
	c.PurgeSelector(key, false)
	if err = c.UpdateHTTPInfo(context.Background(), token, r2, &updated); !errors.Is(err, ErrInvalidated) {
		t.Fatalf("post purge refresh=%v", err)
	}
}

func TestMetadataRefreshRejectsAnotherSelectorsToken(t *testing.T) {
	c := safetyCache(t)
	key := []byte("body")
	w, _ := c.OpenWrite(key, 0)
	info := NewCacheHTTPInfo()
	info.ResponseHeaders.Set("X-Version", "old")
	w.SetHTTPInfo(info)
	w.Write([]byte("body"))
	w.Close()
	r, _ := c.OpenReadCommitted(key)
	defer r.Close()
	r.LoadHTTPInfo()
	token, _ := c.BeginFill([]byte("other"), []byte("other"))
	updated := NewCacheHTTPInfo()
	updated.ResponseHeaders.Set("X-Version", "new")
	if err := c.UpdateHTTPInfo(context.Background(), token, r, updated); !errors.Is(err, ErrInvalidated) {
		t.Fatalf("foreign selector update=%v", err)
	}
}

func TestUnsafePolicyFencesEmptyFamilyWithoutCancellingOtherFill(t *testing.T) {
	c := safetyCache(t)
	selector := []byte("unsafe-family")
	old, _ := c.BeginFill(selector, selector)
	unsafe, _ := c.BeginFill(selector, selector)
	unrelated, _ := c.OpenWrite([]byte("independent-download"), 0)
	unrelated.Write([]byte("keep"))
	if err := c.InvalidateFill(unsafe); err != nil {
		t.Fatal(err)
	}
	if err := unrelated.Close(); err != nil {
		t.Fatalf("unsafe policy cancelled unrelated fill: %v", err)
	}
	if w, err := c.OpenWriteFillKey(context.Background(), old, []byte("late-variant"), 0); !errors.Is(err, ErrInvalidated) {
		if w != nil {
			w.Abort(io.EOF)
		}
		t.Fatalf("unsafe policy allowed old empty-family fill: %v", err)
	}
	assertInvalidationBody(t, c, "independent-download", "keep")
}
