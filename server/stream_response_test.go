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
	return withRequestRuntime(r, r.Context(), &runtimeSnapshot{config: config.GetDefaultConfig()})
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
