package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Monsterwi/razor/internal/budget"
)

var ErrOverloaded = budget.ErrOverloaded

type Limits struct {
	Requests, Queued, Connections                                                          int
	QueueTimeout, DialTimeout, TLSHandshakeTimeout, ResponseHeaderTimeout, BodyIdleTimeout time.Duration
}

func DefaultLimits() Limits {
	return Limits{64, 32, 64, time.Second, 5 * time.Second, 5 * time.Second, 30 * time.Second, 30 * time.Second}
}
func (l Limits) defaults() Limits {
	d := DefaultLimits()
	if l.Requests == 0 {
		l.Requests = d.Requests
	}
	if l.Connections == 0 {
		l.Connections = d.Connections
	}
	if l.QueueTimeout == 0 {
		l.QueueTimeout = d.QueueTimeout
	}
	if l.DialTimeout == 0 {
		l.DialTimeout = d.DialTimeout
	}
	if l.TLSHandshakeTimeout == 0 {
		l.TLSHandshakeTimeout = d.TLSHandshakeTimeout
	}
	if l.ResponseHeaderTimeout == 0 {
		l.ResponseHeaderTimeout = d.ResponseHeaderTimeout
	}
	if l.BodyIdleTimeout == 0 {
		l.BodyIdleTimeout = d.BodyIdleTimeout
	}
	return l
}
func WithLimits(l Limits) Option { return func(h *HTTPProxy) error { h.limits = l; return nil } }

// ConfigureLimits must run before serving or starting health checks. Custom
// transports keep ownership of their connection/dial/TLS settings.
func (h *HTTPProxy) ConfigureLimits(l Limits) error {
	l = l.defaults()
	if l.Requests < 1 || l.Queued < 0 || l.Connections < 1 || l.QueueTimeout < 0 || l.DialTimeout < 0 || l.TLSHandshakeTimeout < 0 || l.ResponseHeaderTimeout < 0 || l.BodyIdleTimeout < 0 {
		return fmt.Errorf("invalid upstream resource limits")
	}
	if h.customTransport && h.roots != nil {
		return fmt.Errorf("upstream CA file cannot be combined with a custom transport")
	}
	h.limits = l
	h.budgets = make(map[string]*budget.Limiter)
	for _, target := range h.targets {
		key := originKey(target)
		if h.budgets[key] == nil {
			h.budgets[key] = budget.New(l.Requests, l.Queued)
		}
	}
	if !h.customTransport {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = nil
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: h.roots}
		tr.ForceAttemptHTTP2 = true
		tr.DisableCompression = true
		tr.MaxResponseHeaderBytes = 64 << 10
		tr.MaxConnsPerHost = l.Connections
		tr.MaxIdleConnsPerHost = l.Connections
		tr.MaxIdleConns = l.Connections * len(h.budgets)
		tr.DialContext = (&net.Dialer{Timeout: l.DialTimeout, KeepAlive: 30 * time.Second}).DialContext
		tr.TLSHandshakeTimeout = l.TLSHandshakeTimeout
		tr.ResponseHeaderTimeout = l.ResponseHeaderTimeout
		// RawBody promises the origin representation without transparent decompression.
		h.transport = tr
	}
	return nil
}
func originKey(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(GetHost(u))
}

type Stats struct {
	InFlight, Queued                                              int
	Rejected, Requests, Errors, Bytes, TTFBCount, TTFBNanoseconds uint64
}

func (h *HTTPProxy) Stats() Stats {
	s := Stats{Requests: h.requests.Load(), Errors: h.errors.Load(), Bytes: h.bytes.Load(), TTFBCount: h.ttfbCount.Load(), TTFBNanoseconds: h.ttfbNanos.Load()}
	for _, l := range h.budgets {
		b := l.Stats()
		s.InFlight += b.InFlight
		s.Queued += b.Queued
		s.Rejected += b.Rejected
	}
	return s
}

type counters struct{ requests, errors, bytes, ttfbCount, ttfbNanos atomic.Uint64 }

// StopBackground cancels and joins the optional health checks without canceling
// active downloads; Close also cancels active origin operations and idle pools.
func (h *HTTPProxy) StopBackground() {
	h.healthMu.Lock()
	h.healthStopped = true
	if h.healthCancel != nil {
		h.healthCancel()
	}
	h.healthMu.Unlock()
	h.healthWG.Wait()
}
func (h *HTTPProxy) Close() error {
	h.StopBackground()
	h.cancel()
	if c, ok := h.transport.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
	return nil
}

func (h *HTTPProxy) roundTrip(r *http.Request, host string) (*http.Response, error) {
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(h.ctx, cancel)
	release, err := h.budgets[originKey(h.targets[host])].Acquire(ctx, h.limits.QueueTimeout)
	if err != nil {
		stop()
		cancel()
		return nil, err
	}
	h.lb.Inc(host)
	h.requests.Add(1)
	done := func() { release(); h.lb.Done(host); stop(); cancel() }
	out := r.Clone(ctx)
	// Proxy callers supply the already rewritten request, including its origin URL.
	started := time.Now()
	resp, err := h.transport.RoundTrip(out)
	if err != nil {
		h.errors.Add(1)
		done()
		return nil, err
	}
	h.ttfbCount.Add(1)
	h.ttfbNanos.Add(uint64(time.Since(started)))
	resp.Body = &trackedBody{ReadCloser: resp.Body, done: done, cancel: cancel, idle: h.limits.BodyIdleTimeout, bytes: &h.bytes, errors: &h.errors}
	return resp, nil
}

type originTransport struct {
	h    *HTTPProxy
	host string
}

func (t originTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.h.roundTrip(r, t.host)
}
