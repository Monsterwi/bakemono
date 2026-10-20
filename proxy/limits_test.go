package proxy_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Monsterwi/razor/balancer"
	"github.com/Monsterwi/razor/proxy"
)

func TestOriginBudgetHeldUntilBodyCloses(t *testing.T) {
	release := make(chan struct{})
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "2")
		w.Write([]byte("a"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
			w.Write([]byte("b"))
		case <-r.Context().Done():
		}
	}))
	defer origin.Close()
	defer close(release)
	p, err := proxy.NewHTTPProxy([]string{origin.URL}, balancer.R2Balancer, proxy.WithLimits(proxy.Limits{Requests: 1, Queued: 1, Connections: 1, QueueTimeout: time.Second}))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	request := func(ctx context.Context) *http.Request {
		r, _ := http.NewRequestWithContext(ctx, "GET", "http://front.test/x", nil)
		return r
	}
	first, err := p.Proxy(request(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	if p.Stats().InFlight != 1 {
		t.Fatal("origin slot released at headers")
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := p.Proxy(request(ctx)); result <- err }()
	deadline := time.After(time.Second)
	for p.Stats().Queued != 1 {
		select {
		case <-deadline:
			t.Fatal("request never queued")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	_, err = p.Proxy(request(context.Background()))
	if !errors.Is(err, proxy.ErrOverloaded) {
		t.Fatalf("overload = %v", err)
	}
	cancel()
	if err = <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
	first.RawBody().Close()
	second, err := p.Proxy(request(context.Background()))
	if err != nil {
		t.Fatal(err)
	}
	second.RawBody().Close()
	if st := p.Stats(); st.InFlight != 0 || st.Queued != 0 || st.Rejected != 1 {
		t.Fatalf("leaked budget: %+v", st)
	}
}

func TestOriginIdleTimeoutReleasesBodyBudget(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer origin.Close()
	p, err := proxy.NewHTTPProxy([]string{origin.URL}, balancer.R2Balancer, proxy.WithLimits(proxy.Limits{Requests: 1, Queued: 1, Connections: 1, BodyIdleTimeout: 40 * time.Millisecond}))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r, _ := http.NewRequest("GET", "http://front.test", nil)
	resp, err := p.Proxy(r)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(resp.RawBody())
	resp.RawBody().Close()
	if err == nil {
		t.Fatal("stalled origin did not time out")
	}
	if p.Stats().InFlight != 0 {
		t.Fatal("timed out body leaked slot")
	}
}

func TestOriginPhaseTimeoutsAllowProgressingBody(t *testing.T) {
	t.Run("response headers", func(t *testing.T) {
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
		defer origin.Close()
		p, err := proxy.NewHTTPProxy([]string{origin.URL}, balancer.R2Balancer, proxy.WithLimits(proxy.Limits{ResponseHeaderTimeout: 30 * time.Millisecond}))
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		r, _ := http.NewRequest("GET", "http://front.test", nil)
		if _, err = p.Proxy(r); err == nil {
			t.Fatal("missing header timeout")
		}
		if p.Stats().InFlight != 0 {
			t.Fatal("header timeout leaked slot")
		}
	})
	t.Run("progress resets idle budget", func(t *testing.T) {
		origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for i := 0; i < 10; i++ {
				w.Write([]byte("x"))
				w.(http.Flusher).Flush()
				time.Sleep(30 * time.Millisecond)
			}
		}))
		defer origin.Close()
		p, err := proxy.NewHTTPProxy([]string{origin.URL}, balancer.R2Balancer, proxy.WithLimits(proxy.Limits{BodyIdleTimeout: 200 * time.Millisecond}))
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		r, _ := http.NewRequest("GET", "http://front.test", nil)
		resp, err := p.Proxy(r)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.RawBody())
		resp.RawBody().Close()
		if err != nil || string(body) != "xxxxxxxxxx" {
			t.Fatalf("progressing body=%q err=%v", body, err)
		}
	})
}

func TestCloseStopsHealthChecksAndActiveOrigin(t *testing.T) {
	connections := make(chan struct{}, 8)
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	origin.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections <- struct{}{}
		}
	}
	origin.Start()
	defer origin.Close()
	p, err := proxy.NewHTTPProxy([]string{origin.URL}, balancer.R2Balancer)
	if err != nil {
		t.Fatal(err)
	}
	p.HealthCheck(1)
	select {
	case <-connections:
	case <-time.After(5 * time.Second):
		t.Fatal("health check did not run")
	}
	r, _ := http.NewRequest("GET", "http://front.test", nil)
	response, err := p.Proxy(r)
	if err != nil {
		t.Fatal(err)
	}
	<-connections
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(response.RawBody()); err == nil {
		t.Fatal("close did not cancel active origin")
	}
	response.RawBody().Close()
	select {
	case <-connections:
		t.Fatal("health check survived Close")
	case <-time.After(1100 * time.Millisecond):
	}
	if p.Stats().InFlight != 0 {
		t.Fatal("close leaked origin slot")
	}
}
