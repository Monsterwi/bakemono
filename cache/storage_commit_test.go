package cache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func openStorageTest(t *testing.T, path string, size, skip int64, ram uint64) (*Cache, *Stripe) {
	t.Helper()
	s := NewStripe(path, size, skip, WithRamCache(ram))
	if err := s.Init(size / 512); err != nil {
		t.Fatal(err)
	}
	if err := s.Open(); err != nil {
		t.Fatal(err)
	}
	c := &Cache{Stripes: []*Stripe{s}}
	t.Cleanup(func() { _ = c.Close() })
	return c, s
}
func writeStorageObject(t *testing.T, c *Cache, key string, data []byte) {
	t.Helper()
	w, err := c.OpenWrite([]byte(key), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
}
func readStorageObject(c *Cache, key string) ([]byte, error) {
	r, err := c.OpenReadCommitted([]byte(key))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func TestStorageWrapAndReopenStayInsideStripe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stripe")
	const size, skip = int64(256 * 1024), int64(4096)
	sentinel := bytes.Repeat([]byte{0x7e}, 4096)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteAt(sentinel, skip+size); err != nil {
		t.Fatal(err)
	}
	f.Close()
	c, s := openStorageTest(t, path, size, skip, 0)
	for round := 0; round < 5; round++ {
		payload := bytes.Repeat([]byte{byte('A' + round)}, int(skip+size-s.Start)-DocHeaderSize)
		key := fmt.Sprintf("exact-%d", round)
		writeStorageObject(t, c, key, payload)
		if err := s.FlushAggBufferSync(); err != nil {
			t.Fatal(err)
		}
		got, err := readStorageObject(c, key)
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("round %d read %d: %v", round, len(got), err)
		}
		if s.WritePos != skip+size {
			t.Fatalf("round %d WritePos=%d, want stripe end %d", round, s.WritePos, skip+size)
		}
		writeStorageObject(t, c, fmt.Sprintf("tiny-%d", round), []byte("tiny"))
		if err := s.FlushAggBufferSync(); err != nil {
			t.Fatal(err)
		}
		got, err = readStorageObject(c, fmt.Sprintf("tiny-%d", round))
		if err != nil || string(got) != "tiny" {
			t.Fatalf("tiny read after exact boundary: %q %v", got, err)
		}
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	c, s = openStorageTest(t, path, size, skip, 0)
	for i := 0; i < 10; i++ {
		body := bytes.Repeat([]byte{byte(i)}, 70000)
		key := fmt.Sprintf("after-reopen-%d", i)
		writeStorageObject(t, c, key, body)
		if err := s.FlushAggBufferSync(); err != nil {
			t.Fatal(err)
		}
		got, err := readStorageObject(c, key)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("reopen read %d: %v", i, err)
		}
	}
	got := make([]byte, len(sentinel))
	if _, err = s.Fd.ReadAt(got, skip+size); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, sentinel) {
		t.Fatal("write crossed into adjacent stripe")
	}
}

type faultDisk struct {
	*os.File
	start             int64
	failAt, writes    int
	writeErr, syncErr error
	short             bool
}

func (d *faultDisk) WriteAt(p []byte, off int64) (int, error) {
	if off >= d.start {
		d.writes++
		if d.writes == d.failAt {
			if d.short {
				return len(p) / 2, nil
			}
			return 0, d.writeErr
		}
	}
	return d.File.WriteAt(p, off)
}
func (d *faultDisk) Sync() error {
	if d.syncErr != nil {
		return d.syncErr
	}
	return d.File.Sync()
}

func TestStorageFailuresCannotCommitObjects(t *testing.T) {
	for _, tc := range []struct {
		name  string
		err   error
		short bool
	}{{"short", io.ErrShortWrite, true}, {"ENOSPC", syscall.ENOSPC, false}, {"EIO", syscall.EIO, false}} {
		for _, failAt := range []int{1, 2, 3} {
			t.Run(fmt.Sprintf("%s/fragment-%d", tc.name, failAt), func(t *testing.T) {
				c, s := openStorageTest(t, filepath.Join(t.TempDir(), "stripe"), 16<<20, 0, 4<<20)
				s.disk = &faultDisk{File: s.Fd, start: s.Start, failAt: failAt, writeErr: tc.err, short: tc.short}
				w, err := c.OpenWrite([]byte("failed-multipart"), 0)
				if err != nil {
					t.Fatal(err)
				}
				_, writeErr := w.Write(bytes.Repeat([]byte{'X'}, 2*TargetFragmentSize+17))
				closeErr := w.Close()
				if !errors.Is(errors.Join(writeErr, closeErr), tc.err) {
					t.Fatalf("Write=%v Close=%v, want %v", writeErr, closeErr, tc.err)
				}
				expected := s.Start
				if failAt >= 2 {
					expected += TargetFragmentSize + DocHeaderSize
				}
				if failAt >= 3 {
					expected += 17 + DocHeaderSize
				}
				if s.WritePos != expected {
					t.Fatalf("failed write advanced committed position: got %d want %d", s.WritePos, expected)
				}
				if _, err := readStorageObject(c, "failed-multipart"); err == nil {
					t.Fatal("failed object readable")
				}
				if err := s.FlushAggBufferSync(); !errors.Is(err, tc.err) {
					t.Fatalf("flush=%v", err)
				}
				if _, err := c.OpenWrite([]byte("later"), 0); !errors.Is(err, tc.err) {
					t.Fatalf("later write admission=%v", err)
				}
				if err := c.Close(); !errors.Is(err, tc.err) {
					t.Fatalf("close=%v", err)
				}
				if s.Fd != nil {
					t.Fatal("failed stripe file left open")
				}
			})
		}
	}
}

func TestCommittedReaderNeverMixesReplacementFragments(t *testing.T) {
	for _, ram := range []uint64{0, 4 << 20} {
		t.Run(fmt.Sprintf("ram-%d", ram), func(t *testing.T) {
			c, _ := openStorageTest(t, filepath.Join(t.TempDir(), "stripe"), 16<<20, 0, ram)
			key := []byte("replacement-reader")
			writeStorageObject(t, c, string(key), bytes.Repeat([]byte("a"), 2*TargetFragmentSize))
			r, err := c.OpenReadCommitted(key)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if _, err = r.LoadHTTPInfo(); err != nil {
				t.Fatal(err)
			}
			newer, err := c.OpenWrite(key, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = newer.Write(bytes.Repeat([]byte("b"), 2*TargetFragmentSize)); err != nil {
				t.Fatal(err)
			}
			defer newer.Abort(io.ErrClosedPipe)
			got, err := io.ReadAll(r)
			if err == nil || bytes.Contains(got, []byte("b")) {
				t.Fatalf("replacement must fail safely, got len=%d err=%v", len(got), err)
			}
		})
	}
}

func TestCommittedReplacementReadErrorIsTerminal(t *testing.T) {
	for _, ram := range []uint64{0, 4 << 20} {
		t.Run(fmt.Sprintf("ram-%d", ram), func(t *testing.T) {
			c, _ := openStorageTest(t, filepath.Join(t.TempDir(), "stripe"), 16<<20, 0, ram)
			key := []byte("committed-replacement-reader")
			writeStorageObject(t, c, string(key), bytes.Repeat([]byte("a"), 2*TargetFragmentSize))
			r, err := c.OpenReadCommitted(key)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if _, err = r.LoadHTTPInfo(); err != nil {
				t.Fatal(err)
			}
			writeStorageObject(t, c, string(key), bytes.Repeat([]byte("b"), 2*TargetFragmentSize))

			buf := make([]byte, 2*TargetFragmentSize)
			n, firstErr := r.Read(buf)
			if n != TargetFragmentSize || !bytes.Equal(buf[:n], bytes.Repeat([]byte("a"), n)) {
				t.Errorf("read returned replacement bytes or wrong prefix length: n=%d", n)
			}
			if firstErr == nil || firstErr == io.EOF {
				t.Errorf("cross-fragment read hid replacement failure: %v", firstErr)
			}
			for range 2 {
				if n, err := r.Read(buf); n != 0 || err == nil || err == io.EOF || err != firstErr {
					t.Errorf("terminal failure changed or exposed more data: n=%d err=%v first=%v", n, err, firstErr)
				}
			}
		})
	}
}

func TestStorageSaveFailureClosesEveryStripe(t *testing.T) {
	c, a := openStorageTest(t, filepath.Join(t.TempDir(), "a"), 1<<20, 0, 0)
	_, b := openStorageTest(t, filepath.Join(t.TempDir(), "b"), 1<<20, 0, 0)
	c.Stripes = append(c.Stripes, b)
	a.disk = &faultDisk{File: a.Fd, start: a.Start, syncErr: syscall.EIO}
	if err := a.Save(); !errors.Is(err, syscall.EIO) {
		t.Fatalf("Save=%v", err)
	}
	if err := c.Close(); !errors.Is(err, syscall.EIO) {
		t.Fatalf("Close=%v", err)
	}
	if a.Fd != nil || b.Fd != nil {
		t.Fatal("Close left a stripe open after an earlier failure")
	}
}

func TestCacheWriterCancellationExitsWaitForDisk(t *testing.T) {
	c, s := openStorageTest(t, filepath.Join(t.TempDir(), "stripe"), 8<<20, 0, 0)
	block := &blockingDisk{File: s.Fd, start: s.Start, entered: make(chan struct{}), release: make(chan struct{})}
	s.disk = block
	ctx, cancel := context.WithCancel(context.Background())
	w, err := c.OpenWriteContext(ctx, []byte("cancelled"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte("body")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- w.Close() }()
	<-block.entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Close=%v", err)
		}
	case <-time.After(time.Second):
		close(block.release)
		t.Fatal("context cancellation did not stop cache wait")
	}
	close(block.release)
	if err := s.FlushAggBufferSync(); err != nil {
		t.Fatal(err)
	}
	if _, err := readStorageObject(c, "cancelled"); err == nil {
		t.Fatal("cancelled object published")
	}
}

type blockingDisk struct {
	*os.File
	start            int64
	entered, release chan struct{}
	once             sync.Once
}

func (d *blockingDisk) WriteAt(p []byte, off int64) (int, error) {
	if off >= d.start {
		d.once.Do(func() { close(d.entered); <-d.release })
	}
	return d.File.WriteAt(p, off)
}

func TestCacheCancellationDuringCheckpointSync(t *testing.T) {
	c, s := openStorageTest(t, filepath.Join(t.TempDir(), "stripe"), 8<<20, 0, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := c.OpenWriteContext(ctx, []byte("cancel-during-save"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(bytes.Repeat([]byte{'p'}, 2*TargetFragmentSize)); err != nil {
		t.Fatal(err)
	}
	d := &blockingSyncDisk{File: s.Fd, entered: make(chan struct{}), release: make(chan struct{})}
	s.disk = d
	saved := make(chan error, 1)
	go func() { saved <- s.Save() }()
	<-d.entered
	cancel()
	done := make(chan error, 1)
	go func() { done <- w.Close() }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Close=%v", err)
		}
	case <-time.After(time.Second):
		close(d.release)
		<-saved
		t.Fatal("checkpoint Sync blocked cancellation")
	}
	close(d.release)
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	if _, err := readStorageObject(c, "cancel-during-save"); err == nil {
		t.Fatal("cancelled checkpoint object readable")
	}
}

type blockingSyncDisk struct {
	*os.File
	entered, release chan struct{}
	once             sync.Once
}

func (d *blockingSyncDisk) Sync() error {
	d.once.Do(func() { close(d.entered); <-d.release })
	return d.File.Sync()
}

func TestWriterRejectsObjectLargerThanStripeBeforeCommit(t *testing.T) {
	c, _ := openStorageTest(t, filepath.Join(t.TempDir(), "stripe"), 256<<10, 0, 0)
	w, err := c.OpenWrite([]byte("too-large"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(make([]byte, 300<<10)); err == nil {
		t.Fatal("Write accepted an object larger than stripe data area")
	}
	if err = w.Close(); err == nil {
		t.Fatal("oversized object committed")
	}
}

func TestConcurrentWriteFlushAndSave(t *testing.T) {
	c, s := openStorageTest(t, filepath.Join(t.TempDir(), "stripe"), 32<<20, 0, 0)
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				key := fmt.Sprintf("concurrent/%d/%d", worker, i)
				body := bytes.Repeat([]byte{byte(worker)}, 16000)
				w, err := c.OpenWrite([]byte(key), 0)
				if err != nil {
					errs <- err
					return
				}
				if _, err = w.Write(body); err != nil {
					errs <- err
					return
				}
				if err = w.Close(); err != nil {
					errs <- err
					return
				}
				got, err := readStorageObject(c, key)
				if err != nil || !bytes.Equal(got, body) {
					errs <- fmt.Errorf("read %s: %w", key, err)
					return
				}
			}
		}(worker)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			if err := s.FlushAggBufferSync(); err != nil {
				errs <- err
				return
			}
			if err := s.Save(); err != nil {
				errs <- err
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent save/flush/writes stalled")
	}
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	saved := make(chan error, 1)
	go func() { saved <- s.Save() }()
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-saved; err != nil && !errors.Is(err, ErrCacheClosed) {
		t.Fatal(err)
	}
}

func TestOldAbortCannotDeleteReplacementAtReusedOffset(t *testing.T) {
	c, s := openStorageTest(t, filepath.Join(t.TempDir(), "stripe"), 3<<20, 0, 4<<20)
	key := []byte("reused-offset")
	old, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = old.Write(bytes.Repeat([]byte{'a'}, 2*TargetFragmentSize)); err != nil {
		t.Fatal(err)
	}
	if err = c.Remove(key); err != nil {
		t.Fatal(err)
	}
	// Complete this lap, then overwrite an entire lap. The replacement's first
	// non-head fragment reuses the old fragment's offset and one-bit phase.
	remaining := s.Skip + s.Len - s.AggBuffer.GetWriteCursor()
	if _, err = s.AggBuffer.Add(make([]byte, remaining), int(remaining), nil); err != nil {
		t.Fatal(err)
	}
	if err = s.FlushAggBufferSync(); err != nil {
		t.Fatal(err)
	}
	capacity := s.Skip + s.Len - s.Start
	if _, err = s.AggBuffer.Add(make([]byte, capacity), int(capacity), nil); err != nil {
		t.Fatal(err)
	}
	if err = s.FlushAggBufferSync(); err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte{'b'}, 2*TargetFragmentSize)
	writeStorageObject(t, c, string(key), body)
	old.Abort(io.ErrUnexpectedEOF)
	got, err := readStorageObject(c, string(key))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("old abort deleted replacement at reused offset: %v bytes=%d", err, len(got))
	}
}
