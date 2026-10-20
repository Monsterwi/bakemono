package server_test

import (
	"errors"
	"github.com/Monsterwi/razor/proxy"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Monsterwi/razor/config"
	"github.com/Monsterwi/razor/server"
)

func TestReloadKeepsDownloadAndRejectsBusyTransition(t *testing.T) {
	release := make(chan struct{})
	oldOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		io.WriteString(w, "old-")
		http.NewResponseController(w).Flush()
		select {
		case <-release:
			io.WriteString(w, "complete")
		case <-r.Context().Done():
		}
	}))
	defer oldOrigin.Close()
	newOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "new") }))
	defer newOrigin.Close()
	cfg := config.GetDefaultConfig()
	s := lifecycleServer(t, oldOrigin.URL, cfg)
	frontend := httptest.NewServer(s)
	defer frontend.Close()
	response, err := frontend.Client().Get(frontend.URL + "/held")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	prefix := make([]byte, 4)
	if _, err := io.ReadFull(response.Body, prefix); err != nil {
		t.Fatal(err)
	}
	candidate := *cfg
	candidate.UpstreamTargets = []string{newOrigin.URL}
	if err := s.Reload(&candidate); err != nil {
		t.Fatal(err)
	}
	if st := s.Stats(); st.RuntimeVersion != 2 || st.RetiredRuntimes != 1 || st.InFlight != 1 || st.Origin.InFlight != 1 {
		t.Fatalf("transition stats: %+v", st)
	}
	if got := fetch(s, "http://cache.test/new", nil); got.Body.String() != "new" {
		t.Fatal(got.Body.String())
	}
	if err := s.Reload(cfg); !errors.Is(err, server.ErrReloadBusy) {
		t.Fatalf("busy reload: %v", err)
	}
	close(release)
	suffix, err := io.ReadAll(response.Body)
	if err != nil || string(prefix)+string(suffix) != "old-complete" {
		t.Fatalf("download interrupted: %q %q %v", prefix, suffix, err)
	}
	deadline := time.Now().Add(time.Second)
	for s.Stats().RetiredRuntimes != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := s.Reload(&candidate); err != nil {
		t.Fatal(err)
	}
	if st := s.Stats(); st.RuntimeVersion != 3 || st.RetiredRuntimes != 0 || st.Origin.Requests != 2 {
		t.Fatalf("drained stats: %+v", st)
	}
	bad := candidate
	bad.UpstreamTargets = []string{"file:///secret"}
	if err := s.Reload(&bad); err == nil {
		t.Fatal("accepted invalid upstream")
	}
	bad = candidate
	bad.MaxRequests = -1
	if err := s.Reload(&bad); err == nil {
		t.Fatal("accepted negative budget")
	}
	bad = candidate
	bad.ServerPort++
	if err := s.Reload(&bad); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("listener reload: %v", err)
	}
	if st := s.Stats(); st.RuntimeVersion != 3 {
		t.Fatalf("failed candidate advanced version: %+v", st)
	}
}

func TestReloadOwnsConfigAndSeparatesCacheNamespaces(t *testing.T) {
	var oldCalls, newCalls atomic.Int64
	oldOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		oldCalls.Add(1)
		w.Header().Set("Cache-Control", "public,max-age=3600")
		io.WriteString(w, "old")
	}))
	defer oldOrigin.Close()
	newOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		newCalls.Add(1)
		w.Header().Set("Cache-Control", "public,max-age=3600")
		io.WriteString(w, "new")
	}))
	defer newOrigin.Close()
	cfg := config.GetDefaultConfig()
	cfg.TrustedProxies = []string{"127.0.0.0/8"}
	s := lifecycleServer(t, oldOrigin.URL, cfg)
	target := "http://cache.test/object"
	if got := fetch(s, target, nil); got.Body.String() != "old" {
		t.Fatal(got.Body.String())
	}
	cfg.UpstreamTargets[0] = newOrigin.URL
	cfg.TrustedProxies[0] = "invalid"
	if got := fetch(s, target, nil); got.Body.String() != "old" || oldCalls.Load() != 1 {
		t.Fatalf("caller mutated active config: body=%q calls=%d", got.Body.String(), oldCalls.Load())
	}
	cfg.TrustedProxies[0] = "127.0.0.0/8"
	if err := s.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	if got := fetch(s, target, nil); got.Body.String() != "new" || newCalls.Load() != 1 {
		t.Fatalf("old namespace reused: %q", got.Body.String())
	}
	cfg.UpstreamTargets[0] = oldOrigin.URL
	if err := s.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	if got := fetch(s, target, nil); got.Body.String() != "old" || oldCalls.Load() != 1 {
		t.Fatalf("identical route lost durable namespace: %q calls=%d", got.Body.String(), oldCalls.Load())
	}
}

func TestReloadRejectsChangedStorageContentsAndCustomTransport(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer origin.Close()
	cfg := config.GetDefaultConfig()
	cfg.ConfigDir = t.TempDir()
	path := filepath.Join(cfg.ConfigDir, "storage.config")
	if err := os.WriteFile(path, []byte("cache.db 32M\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := lifecycleServer(t, origin.URL, cfg)
	if err := os.WriteFile(path, []byte("cache.db 64M\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(cfg); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatalf("changed storage reload: %v", err)
	}
	if s.Stats().RuntimeVersion != 1 {
		t.Fatal("storage changed active runtime")
	}
	custom, err := proxy.NewHTTPProxy(cfg.UpstreamTargets, cfg.UpstreamAlgo, proxy.WithTransport(http.DefaultTransport.(*http.Transport).Clone()))
	if err != nil {
		t.Fatal(err)
	}
	embedded := server.New(cfg, nil, custom)
	defer embedded.Shutdown()
	if err := embedded.Reload(cfg); err == nil || !strings.Contains(err.Error(), "custom") {
		t.Fatalf("custom transport silently replaced: %v", err)
	}
}
