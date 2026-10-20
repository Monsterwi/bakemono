package server_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Monsterwi/razor/cache"
	"github.com/Monsterwi/razor/config"
	"github.com/Monsterwi/razor/proxy"
	"github.com/Monsterwi/razor/server"
)

func purgeServer(t *testing.T, token string, origin http.HandlerFunc) (*server.Server, *cache.Cache) {
	t.Helper()
	upstream := httptest.NewServer(origin)
	t.Cleanup(upstream.Close)
	c, err := cache.NewCache(1, []string{filepath.Join(t.TempDir(), "cache")}, []int64{32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	cfg := config.GetDefaultConfig()
	cfg.PurgeToken = token
	cfg.UpstreamTargets = []string{upstream.URL}
	p, err := proxy.NewHTTPProxy(cfg.UpstreamTargets, cfg.UpstreamAlgo)
	if err != nil {
		t.Fatal(err)
	}
	return server.New(cfg, c, p), c
}

func purge(s http.Handler, target, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("PURGE", target, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestPurgeAuthorizationPrecedesInvalidation(t *testing.T) {
	for _, configured := range []string{"", "test-secret"} {
		t.Run("configured="+fmt.Sprint(configured != ""), func(t *testing.T) {
			var calls atomic.Int32
			s, _ := purgeServer(t, configured, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "public,max-age=3600")
				fmt.Fprint(w, calls.Add(1))
			})
			target := "http://cache.test/private-key?token=sensitive"
			fetch(s, target, nil)
			for _, token := range []string{"", "wrong"} {
				got := purge(s, target, token)
				if got.Code != http.StatusForbidden {
					t.Fatalf("unauthorized PURGE = %d", got.Code)
				}
				if body := fetch(s, target, nil).Body.String(); body != "1" {
					t.Fatalf("unauthorized PURGE changed cached body: %q", body)
				}
			}
			if configured != "" {
				got := purge(s, target, configured)
				if got.Code != http.StatusOK || strings.Contains(got.Body.String(), "private-key") || strings.Contains(got.Body.String(), "sensitive") {
					t.Fatalf("authorized PURGE = %d %q", got.Code, got.Body.String())
				}
				if body := fetch(s, target, nil).Body.String(); body != "2" {
					t.Fatalf("authorized PURGE retained %q", body)
				}
			}
		})
	}
}

func TestPurgeFencesOriginRequestBeforeHeaders(t *testing.T) {
	for _, suffix := range []string{"object", ""} {
		t.Run("purge="+suffix, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			s, _ := purgeServer(t, "test-secret", func(w http.ResponseWriter, r *http.Request) {
				serial := calls.Add(1)
				if serial == 1 {
					close(entered)
					<-release
				}
				w.Header().Set("Cache-Control", "public,max-age=3600")
				fmt.Fprint(w, serial)
			})
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- fetch(s, "http://cache.test/folder/object", nil) }()
			<-entered
			got := purge(s, "http://cache.test/folder/"+suffix, "test-secret")
			if got.Code != http.StatusOK {
				t.Fatal(got.Code)
			}
			close(release)
			if got := <-done; got.Body.String() != "1" {
				t.Fatal(got.Body.String())
			}
			if got := fetch(s, "http://cache.test/folder/object", nil); got.Body.String() != "2" {
				t.Fatalf("pre-PURGE origin republished %q", got.Body.String())
			}
			if got := fetch(s, "http://cache.test/folder/object", nil); got.Body.String() != "2" {
				t.Fatalf("new fill did not cache %q", got.Body.String())
			}
		})
	}
}
