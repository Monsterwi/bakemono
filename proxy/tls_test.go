package proxy_test

import (
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Monsterwi/razor/balancer"
	"github.com/Monsterwi/razor/proxy"
)

func TestConfiguredCAIsUsedForRequestsAndHealth(t *testing.T) {
	checked := make(chan struct{}, 4)
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			checked <- struct{}{}
		}
		io.WriteString(w, "verified")
	}))
	defer origin.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: origin.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := proxy.NewHTTPProxy([]string{origin.URL}, balancer.R2Balancer, proxy.WithCAFile(ca))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.ConfigureLimits(proxy.DefaultLimits()); err != nil {
		t.Fatal(err)
	}
	response, err := p.Proxy(httptest.NewRequest("GET", "http://client/object", nil))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.RawBody())
	response.RawBody().Close()
	if err != nil || string(body) != "verified" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if err := p.StartHealthChecks(proxy.HealthConfig{Interval: time.Second, Timeout: time.Second, Failures: 1, Successes: 1}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-checked:
	case <-time.After(3 * time.Second):
		t.Fatal("verified HTTPS probe did not reach origin")
	}
	p.StopBackground()
	if _, err := proxy.NewHTTPProxy([]string{origin.URL}, balancer.R2Balancer, proxy.WithCAFile(filepath.Join(t.TempDir(), "absent.pem"))); err == nil {
		t.Fatal("missing CA silently accepted")
	}
	if err := os.WriteFile(ca, []byte("not a certificate"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := proxy.NewHTTPProxy([]string{origin.URL}, balancer.R2Balancer, proxy.WithCAFile(ca)); err == nil {
		t.Fatal("invalid CA silently accepted")
	}
}
