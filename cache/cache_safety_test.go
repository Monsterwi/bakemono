package cache

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"
)

func safetyCache(t *testing.T) *Cache {
	t.Helper()
	c, err := NewCache(1, []string{filepath.Join(t.TempDir(), "cache.dat")}, []int64{32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	return c
}

func TestAbortedWriteWakesReadersAndCannotCommit(t *testing.T) {
	c := safetyCache(t)
	key := []byte("aborted-object")
	w, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := c.OpenRead(key)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	cause := errors.New("origin truncated")
	ready := make(chan error, 1)
	go func() { ready <- reader.WaitForHeaders(time.Second) }()
	w.Abort(cause)
	select {
	case err := <-ready:
		if err == nil {
			t.Fatal("headers reported success after abort")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("abort did not wake header waiter")
	}
	if err := w.Close(); !errors.Is(err, cause) {
		t.Fatalf("close hid failure: %v", err)
	}
	if r, err := c.OpenReadCommitted(key); err == nil {
		defer r.Close()
		if _, err = r.LoadHTTPInfo(); err == nil {
			t.Fatal("aborted object became readable")
		}
	}
}

func TestAbortRemovesPublishedFragments(t *testing.T) {
	c := safetyCache(t)
	key := []byte("fragmented-aborted-object")
	w, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(bytes.Repeat([]byte("x"), 2*TargetFragmentSize+5)); err != nil {
		t.Fatal(err)
	}
	w.Abort(io.ErrUnexpectedEOF)
	if hit, _, _ := c.KeyToStripe(key).DirMgr.Get(FragmentKey(key, 1)); hit {
		t.Fatal("aborted fragment remained in directory")
	}
	if _, hit := c.KeyToStripe(key).RamCache.Get(FragmentKey(key, 1)); hit {
		t.Fatal("aborted fragment remained in RAM")
	}
	fragment, _ := c.OpenReadCommitted(FragmentKey(key, 1))
	defer fragment.Close()
	if _, err = fragment.Read(make([]byte, 1)); err == nil {
		t.Fatal("aborted fragment remained readable")
	}
	next, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatalf("abort leaked writer: %v", err)
	}
	next.Abort(io.EOF)
}

func TestReadCloseAndWriteConflictDoNotReleaseOrLeakActiveWriter(t *testing.T) {
	c := safetyCache(t)
	key := []byte("writer-ownership")
	writer, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := c.OpenRead(key)
	if err != nil {
		t.Fatal(err)
	}
	reader.Close()
	if conflict, err := c.OpenWrite(key, 0); err == nil {
		conflict.Abort(io.EOF)
		t.Fatal("reader close released active writer")
	}
	writer.Abort(io.EOF)
	next, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatalf("conflict leaked reference: %v", err)
	}
	next.Abort(io.EOF)
}

func TestEmptyCommittedObjectReadsEOF(t *testing.T) {
	c := safetyCache(t)
	key := []byte("empty")
	w, _ := c.OpenWrite(key, 0)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, _ := c.OpenReadCommitted(key)
	defer r.Close()
	body, err := io.ReadAll(r)
	if err != nil || len(body) != 0 {
		t.Fatalf("empty read = %q,%v", body, err)
	}
}

func TestDirectoryCollisionNeverReturnsAnotherObjectsBody(t *testing.T) {
	c := safetyCache(t)
	// These share the directory bucket/tag and the old first-16-byte identity.
	a, b := []byte("0123456789abcdef/72"), []byte("0123456789abcdef/920")
	w, _ := c.OpenWrite(a, 0)
	w.Write([]byte("object A"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, _ := c.OpenReadCommitted(b)
	defer r.Close()
	body, err := io.ReadAll(r)
	if err == nil {
		t.Fatalf("absent object B returned %q", body)
	}
}

func TestFailedCommitReturnsErrorAndReleasesWriter(t *testing.T) {
	c := safetyCache(t)
	key := []byte("invalid-metadata")
	w, _ := c.OpenWrite(key, 0)
	info := NewCacheHTTPInfo()
	info.RequestURL = string(bytes.Repeat([]byte("x"), 65536))
	w.SetHTTPInfo(info)
	w.Write([]byte("body"))
	if err := w.Close(); err == nil {
		t.Fatal("invalid metadata commit succeeded")
	}
	if err := w.Close(); err == nil {
		t.Fatal("repeated Close hid commit failure")
	}
	next, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatalf("failed commit leaked writer: %v", err)
	}
	next.Abort(io.EOF)
}

func TestLegacyDiskIdentityIsRejected(t *testing.T) {
	c := safetyCache(t)
	key := []byte("legacy-key")
	w, _ := c.OpenWrite(key, 0)
	w.Write([]byte("legacy body"))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	stripe := c.KeyToStripe(key)
	if err := stripe.FlushAggBufferSync(); err != nil {
		t.Fatal(err)
	}
	_, _, dir := stripe.DirMgr.Get(key)
	header := make([]byte, DocHeaderSize)
	if _, err := stripe.Fd.ReadAt(header, int64(dir.offset())); err != nil {
		t.Fatal(err)
	}
	var doc Doc
	if err := doc.UnmarshalBinary(header); err != nil {
		t.Fatal(err)
	}
	doc.VMajor = 0
	header, _ = doc.MarshalBinary()
	if _, err := stripe.Fd.WriteAt(header, int64(dir.offset())); err != nil {
		t.Fatal(err)
	}
	stripe.RamCache.Delete(key)
	r, err := c.OpenReadCommitted(key)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := io.ReadAll(r); err == nil {
		t.Fatal("accepted legacy disk identity")
	}
}

func TestOversizedHeaderCannotCommitCorruptMetadata(t *testing.T) {
	c := safetyCache(t)
	key := []byte("oversized-header")
	w, _ := c.OpenWrite(key, 0)
	info := NewCacheHTTPInfo()
	info.ResponseHeaders.Set("X-Large", string(bytes.Repeat([]byte("x"), 65536)))
	w.SetHTTPInfo(info)
	w.Write([]byte("body"))
	if err := w.Close(); err == nil {
		t.Fatal("oversized header silently truncated in metadata")
	}
}

func TestAbortingInvalidatedWriterDoesNotDeleteReplacement(t *testing.T) {
	c := safetyCache(t)
	key := []byte("replacement-after-invalidation")
	old, _ := c.OpenWrite(key, 0)
	old.Write(bytes.Repeat([]byte("a"), 2*TargetFragmentSize))
	if err := c.Remove(key); err != nil {
		t.Fatal(err)
	}
	newer, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("b"), 2*TargetFragmentSize)
	if _, err = newer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err = newer.Close(); err != nil {
		t.Fatal(err)
	}
	old.Abort(io.ErrUnexpectedEOF)
	r, err := c.OpenReadCommitted(key)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("old abort damaged replacement: bytes=%d err=%v", len(got), err)
	}
}
