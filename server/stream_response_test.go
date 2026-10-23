package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Monsterwi/razor/config"
)

type streamSink struct {
	header http.Header
	short  bool
}

func (w *streamSink) Header() http.Header { return w.header }
func (*streamSink) WriteHeader(int)       {}
func (w *streamSink) Write(p []byte) (int, error) {
	if w.short {
		return len(p) - 1, nil
	}
	return len(p), nil
}
func (*streamSink) Flush()                           {}
func (*streamSink) SetWriteDeadline(time.Time) error { return nil }

func streamRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://cache.test/object", nil)
	return withRequestRuntime(r, r.Context(), &runtimeSnapshot{config: config.GetDefaultConfig(), responseBuffers: newResponseBufferPool(1)})
}

func TestStreamResponseReuseAndErrors(t *testing.T) {
	s := new(Server)
	for _, size := range []int{256 << 10, 7, 1 << 20, 0, 33 << 10} {
		body := bytes.Repeat([]byte{byte(size + 1)}, size)
		w := httptest.NewRecorder()
		if err := s.streamResponse(w, bytes.NewReader(body), streamRequest()); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(w.Body.Bytes(), body) {
			t.Fatalf("response size %d changed", size)
		}
	}
	if err := s.streamResponse(&streamSink{short: true}, bytes.NewReader([]byte("body")), streamRequest()); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write: %v", err)
	}
	r := streamRequest()
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	w := httptest.NewRecorder()
	if err := s.streamResponse(w, bytes.NewReader([]byte("body")), r.WithContext(ctx)); !errors.Is(err, context.Canceled) || w.Body.Len() != 0 {
		t.Fatalf("cancelled response: %v, bytes=%d", err, w.Body.Len())
	}
}

func BenchmarkStreamResponse(b *testing.B) {
	body := make([]byte, 256<<10)
	r := streamRequest()
	s := new(Server)
	w := &streamSink{header: make(http.Header)}
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		if err := s.streamResponse(w, bytes.NewReader(body), r); err != nil {
			b.Fatal(err)
		}
	}
}

type stalledStreamSink struct {
	streamSink
	started, resume chan struct{}
}

func (w *stalledStreamSink) Write(p []byte) (int, error) {
	close(w.started)
	<-w.resume
	return len(p), nil
}

type flushErrorSink struct{ streamSink }

func (*flushErrorSink) FlushError() error { return io.ErrClosedPipe }

func TestStreamResponseBufferBudgetCancellationAndReuse(t *testing.T) {
	s := new(Server)
	r := streamRequest()
	pool := requestRuntime(r).responseBuffers
	first := &stalledStreamSink{started: make(chan struct{}), resume: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- s.streamResponse(first, bytes.NewReader([]byte("first")), r) }()
	<-first.started
	if pool.allocated.Load() != responseBufferSize || pool.inUse.Load() != responseBufferSize {
		t.Fatal("first response was not reserved")
	}
	ctx, cancel := context.WithCancel(r.Context())
	second := make(chan error, 1)
	go func() {
		second <- s.streamResponse(&streamSink{}, bytes.NewReader([]byte("second")), r.WithContext(ctx))
	}()
	cancel()
	if err := <-second; !errors.Is(err, context.Canceled) {
		t.Fatalf("waiting response: %v", err)
	}
	if pool.allocated.Load() != responseBufferSize {
		t.Fatal("allocated beyond admitted response budget")
	}
	close(first.resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for range 20 {
		if err := s.streamResponse(&streamSink{}, bytes.NewReader([]byte("reused")), r); err != nil {
			t.Fatal(err)
		}
	}
	if pool.allocated.Load() != responseBufferSize || pool.inUse.Load() != 0 {
		t.Fatal("buffer was leaked or not reused")
	}
	if err := s.streamResponse(&flushErrorSink{}, bytes.NewReader([]byte("error")), r); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("flush error: %v", err)
	}
	if pool.inUse.Load() != 0 {
		t.Fatal("flush error leaked a buffer")
	}
	if err := s.streamResponse(&streamSink{short: true}, bytes.NewReader([]byte("short")), r); !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(err)
	}
	if pool.inUse.Load() != 0 {
		t.Fatal("short write leaked a buffer")
	}
}
