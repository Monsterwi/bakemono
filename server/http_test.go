package server

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestBodyCloseStopsDeadlineUpdates(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	reading := make(chan struct{})
	response := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	body := &requestBody{
		ReadCloser: &signalledBody{ReadCloser: reader, reading: reading},
		controller: http.NewResponseController(response),
		idle:       time.Second,
	}
	done := make(chan error, 1)
	go func() {
		_, err := body.Read(make([]byte, 1))
		done <- err
	}()
	<-reading
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("blocked read after close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not unblock the request body reader")
	}
	calls := response.calls.Load()
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			if _, err := body.Read(make([]byte, 1)); !errors.Is(err, io.ErrClosedPipe) {
				t.Errorf("read after close: %v", err)
			}
			if err := body.Close(); err != nil {
				t.Errorf("repeated close: %v", err)
			}
		})
	}
	workers.Wait()
	if got := response.calls.Load(); got != calls {
		t.Fatalf("response writer used after body close: deadline calls %d -> %d", calls, got)
	}
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	calls atomic.Int64
}

func (w *deadlineRecorder) SetReadDeadline(time.Time) error {
	w.calls.Add(1)
	return nil
}

type signalledBody struct {
	io.ReadCloser
	reading chan struct{}
	once    sync.Once
}

func (b *signalledBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.reading) })
	return b.ReadCloser.Read(p)
}
