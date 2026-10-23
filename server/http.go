package server

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"
)

// ServeHTTP applies the same request budgets and cancellation as the standalone
// listener. Embedders configure their listener's header and keep-alive deadlines.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	if s.draining || s.initErr != nil {
		s.mu.Unlock()
		s.counters.rejected.Add(1)
		http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
		return
	}
	runtime := s.current
	runtime.refs++
	s.handlers.Add(1)
	s.mu.Unlock()
	started := time.Now()
	defer s.handlers.Done()
	defer s.releaseRuntime(runtime)
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(s.ctx, cancel)
	defer func() { stop(); cancel() }()
	// This context already descends from the request; producers clone before mutation.
	r = r.WithContext(context.WithValue(ctx, runtimeContextKey{}, runtime))
	release, err := runtime.admission.Acquire(ctx, time.Duration(runtime.config.QueueTimeout)*time.Second)
	if err != nil {
		if ctx.Err() != nil {
			s.counters.cancelled.Add(1)
		}
		s.counters.rejected.Add(1)
		http.Error(w, "Service busy", http.StatusServiceUnavailable)
		return
	}
	defer release()
	s.counters.requests.Add(1)
	defer func() {
		if ctx.Err() != nil {
			s.counters.cancelled.Add(1)
		}
	}()
	tracked := &responseMetrics{ResponseWriter: w, stats: &s.counters, start: started}
	// Do not drain a slow upload before sending the origin response.
	_ = http.NewResponseController(tracked).EnableFullDuplex()
	defer http.NewResponseController(tracked).SetWriteDeadline(time.Time{})
	defer http.NewResponseController(tracked).SetReadDeadline(time.Time{})
	if r.Body != nil && r.Body != http.NoBody {
		r.Body = &requestBody{ReadCloser: r.Body, controller: http.NewResponseController(tracked), idle: time.Duration(runtime.config.TransactionTimeoutIn) * time.Second}
		defer r.Body.Close()
	}
	s.handler(tracked, r)
}

type requestBody struct {
	io.ReadCloser
	mu         sync.Mutex
	closed     bool
	controller *http.ResponseController
	idle       time.Duration
}

func (b *requestBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	_ = b.controller.SetReadDeadline(time.Now().Add(b.idle))
	b.mu.Unlock()
	return b.ReadCloser.Read(p)
}

func (b *requestBody) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	// The origin transport may still read or close this body after ServeHTTP returns.
	b.closed = true
	_ = b.controller.SetReadDeadline(time.Now())
	return b.ReadCloser.Close()
}
