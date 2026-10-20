package proxy_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Monsterwi/razor/balancer"
	"github.com/Monsterwi/razor/proxy"
)

func TestHealthThresholdsRecoveryAndStop(t *testing.T) {
	statuses := make(chan int)
	started := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	var probes atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "HEAD" || r.URL.Path != "/ready" {
			t.Errorf("probe = %s %s", r.Method, r.URL)
			w.WriteHeader(500)
			return
		}
		probes.Add(1)
		started <- struct{}{}
		select {
		case status := <-statuses:
			w.WriteHeader(status)
		case <-r.Context().Done():
			cancelled <- struct{}{}
		}
	}))
	defer origin.Close()
	p, err := proxy.NewHTTPProxy([]string{origin.URL}, balancer.R2Balancer)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.StartHealthChecks(proxy.HealthConfig{Interval: 5 * time.Millisecond, Timeout: time.Second, Failures: 2, Successes: 2, Path: "/ready"}); err != nil {
		t.Fatal(err)
	}
	waitProbe := func() {
		t.Helper()
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("probe did not start")
		}
	}
	request := httptest.NewRequest("GET", "http://client/object", nil)
	waitProbe()
	statuses <- 503
	waitProbe() // The second probe starts only after the first result was processed.
	if _, err := p.SelectOrigin(request); err != nil {
		t.Fatal("one failure removed origin")
	}
	statuses <- 503
	waitProbe()
	if _, err := p.SelectOrigin(request); err == nil {
		t.Fatal("failed origin still selectable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if response, err := p.Proxy(request.WithContext(ctx)); err == nil {
		response.RawBody().Close()
		t.Fatal("empty healthy set succeeded")
	}
	statuses <- 200
	waitProbe()
	if _, err := p.SelectOrigin(request); err == nil {
		t.Fatal("one success restored origin")
	}
	statuses <- 200
	waitProbe()
	if _, err := p.SelectOrigin(request); err != nil {
		t.Fatal("recovered origin not selectable")
	}
	p.StopBackground() // Cancels the blocked HTTP HEAD and joins the probe loop.
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("stop left active health request")
	}
	count := probes.Load()
	if err := p.StartHealthChecks(proxy.HealthConfig{Interval: time.Millisecond, Timeout: time.Second, Failures: 1, Successes: 1}); err == nil {
		t.Fatal("restarted stopped checks")
	}
	time.Sleep(20 * time.Millisecond)
	if probes.Load() != count {
		t.Fatal("checks survived StopBackground")
	}
}

func TestHealthProbeDeadlineRemovesStalledOrigin(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		select {
		case cancelled <- struct{}{}:
		default:
		}
	}))
	defer origin.Close()
	p, err := proxy.NewHTTPProxy([]string{origin.URL}, balancer.R2Balancer)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.StartHealthChecks(proxy.HealthConfig{Interval: time.Second, Timeout: 30 * time.Millisecond, Failures: 1, Successes: 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("health deadline did not cancel stalled request")
	}
	deadline := time.After(time.Second)
	for {
		_, err := p.SelectOrigin(httptest.NewRequest("GET", "http://client/object", nil))
		if err != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed-out origin still selectable")
		case <-time.After(time.Millisecond):
		}
	}
	p.StopBackground()
}

func TestZeroHealthIntervalDoesNotProbe(t *testing.T) {
	var calls atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer origin.Close()
	p, err := proxy.NewHTTPProxy([]string{origin.URL}, balancer.R2Balancer)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.StartHealthChecks(proxy.HealthConfig{}); err != nil {
		t.Fatal(err)
	}
	p.HealthCheck(0)
	time.Sleep(20 * time.Millisecond)
	p.StopBackground()
	if calls.Load() != 0 {
		t.Fatal("disabled checks made an origin request")
	}
}

func TestConfiguredUpstreamsSurviveHealthRemoval(t *testing.T) {
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer failed.Close()
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer healthy.Close()
	p, err := proxy.NewHTTPProxy([]string{failed.URL, healthy.URL}, balancer.R2Balancer)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.StartHealthChecks(proxy.HealthConfig{Interval: time.Millisecond, Timeout: time.Second, Failures: 1, Successes: 1}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for p.ReadAlive(failed.URL) {
		if time.Now().After(deadline) {
			t.Fatal("unhealthy origin not removed")
		}
		time.Sleep(time.Millisecond)
	}
	targets, algorithm := p.Upstreams()
	if len(targets) != 2 || targets[0] != failed.URL || targets[1] != healthy.URL || algorithm != balancer.R2Balancer {
		t.Fatalf("configured pool changed with health: %v %q", targets, algorithm)
	}
	targets[0] = "changed by caller"
	again, _ := p.Upstreams()
	if again[0] != failed.URL {
		t.Fatal("caller mutated configured pool")
	}
}
