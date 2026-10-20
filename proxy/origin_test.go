package proxy_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Monsterwi/razor/balancer"
	"github.com/Monsterwi/razor/proxy"
)

func TestSelectedOriginPinsFullTargetAcrossRequests(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Seen-URI", r.URL.RequestURI())
		w.Header().Set("Connection", "X-Hop")
		w.Header().Set("X-Hop", "origin-only")
		io.WriteString(w, "pinned")
	}))
	defer origin.Close()
	targets := []string{origin.URL + "/one?fixed=1", origin.URL + "/two?fixed=2"}
	p, err := proxy.NewHTTPProxy(targets, balancer.R2Balancer)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	request := httptest.NewRequest("GET", "http://client/object?part=1", nil)
	selected, err := p.SelectOrigin(request)
	if err != nil {
		t.Fatal(err)
	}
	identity := selected.Identity()
	if identity != targets[0] && identity != targets[1] {
		t.Fatalf("identity = %q", identity)
	}
	other, err := p.SelectOrigin(request)
	if err != nil {
		t.Fatal(err)
	}
	if other.Identity() == identity {
		t.Fatal("round robin did not select another target")
	}
	for i := 0; i < 3; i++ {
		response, headers, err := selected.ProxyWithOriginHeaders(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.RawBody())
		response.RawBody().Close()
		wantURI := "/one/object?fixed=1&part=1"
		if identity == targets[1] {
			wantURI = "/two/object?fixed=2&part=1"
		}
		if err != nil || string(body) != "pinned" || response.Header().Get("Seen-URI") != wantURI {
			t.Fatalf("response=%q URI=%q error=%v", body, response.Header().Get("Seen-URI"), err)
		}
		if headers.Get("X-Hop") != "origin-only" || response.Header().Get("X-Hop") != "" {
			t.Fatal("origin headers were not preserved separately")
		}
	}
}
