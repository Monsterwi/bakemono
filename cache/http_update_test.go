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

func TestUnsafePolicyCancelsReservedWriterButPreservesReplacement(t *testing.T) {
	c := safetyCache(t)
	key := []byte("unsafe-family")
	old, _ := c.BeginFill(key, key)
	unsafe, _ := c.BeginFill(key, key)
	writer, err := c.OpenWriteFillKeyReplacing(context.Background(), old, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	writer.Write([]byte("old"))
	unrelated, _ := c.OpenWrite([]byte("independent-download"), 0)
	unrelated.Write([]byte("keep"))
	if err = c.InvalidateFill(unsafe); err != nil {
		t.Fatal(err)
	}
	replacement, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	replacement.Write([]byte("new"))
	if err = replacement.Close(); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err == nil {
		t.Fatal("unsafe policy allowed reserved writer to commit")
	}
	if err = unrelated.Close(); err != nil {
		t.Fatalf("unsafe policy cancelled unrelated writer: %v", err)
	}
	assertInvalidationBody(t, c, string(key), "new")
	assertInvalidationBody(t, c, "independent-download", "keep")
}

func TestUnsafePolicyPreservesNewlyCommittedHeadWithReaderReference(t *testing.T) {
	c := safetyCache(t)
	key := []byte("new-committed")
	unsafe, _ := c.BeginFill(key, key)
	writer, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	writer.Write([]byte("new"))
	reader, err := c.OpenRead(key)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = c.InvalidateFill(unsafe); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil || string(body) != "new" {
		t.Fatalf("completed writer reader=%q err=%v", body, err)
	}
	reader.Close()
	assertInvalidationBody(t, c, string(key), "new")
}

func TestUnsafePolicyCannotInvalidatePostPurgeWriter(t *testing.T) {
	c := safetyCache(t)
	key := []byte("post-purge")
	unsafe, _ := c.BeginFill(key, key)
	if _, err := c.PurgeSelector(key, false); err != nil {
		t.Fatal(err)
	}
	writer, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	writer.Write([]byte("new"))
	if err = c.InvalidateFill(unsafe); !errors.Is(err, ErrInvalidated) {
		t.Fatalf("pre-PURGE invalidation=%v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("pre-PURGE token cancelled newer writer: %v", err)
	}
	assertInvalidationBody(t, c, string(key), "new")
}

func TestUnsafePolicyFencesReservedWriterAfterStorageWrap(t *testing.T) {
	c := safetyCache(t)
	key := []byte("unsafe-family")
	unsafe, _ := c.BeginFill(key, key)
	writer, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	writer.Write([]byte("old"))
	// Exceed the fixture's 32 MiB capacity through ordinary writes.
	padding := bytes.Repeat([]byte("x"), 1<<20)
	for range 40 {
		filler, err := c.OpenWrite([]byte("wrap-filler"), 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = filler.Write(padding); err != nil {
			t.Fatal(err)
		}
		if err = filler.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err = c.InvalidateFill(unsafe); err != nil {
		t.Fatalf("storage wrap prevented unsafe-policy fence: %v", err)
	}
	if err = writer.Close(); err == nil {
		t.Fatal("reserved writer escaped policy fence after storage wrap")
	}
}
