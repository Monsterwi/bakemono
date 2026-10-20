package server_test

import (
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/Monsterwi/razor/cache"
	"github.com/Monsterwi/razor/proxy"
	"io"
	"net"
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

func TestReloadUsesActualOriginTrustContents(t *testing.T) {
	var calls atomic.Int64
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public,max-age=3600")
		fmt.Fprint(w, calls.Add(1))
	}))
	defer origin.Close()
	ca := filepath.Join(t.TempDir(), "roots.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: origin.Certificate().Raw})
	if err := os.WriteFile(ca, pemBytes, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.GetDefaultConfig()
	cfg.UpstreamTargets = []string{origin.URL}
	cfg.UpstreamCAFile = ca
	c, err := cache.NewCache(1, []string{filepath.Join(t.TempDir(), "cache")}, []int64{32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Init(); err != nil {
		t.Fatal(err)
	}
	p, err := proxy.NewHTTPProxy(cfg.UpstreamTargets, cfg.UpstreamAlgo, proxy.WithCAFile(ca))
	if err != nil {
		t.Fatal(err)
	}
	s := server.New(cfg, c, p)
	defer s.Shutdown()
	target := "http://front.test/object"
	if got := fetch(s, target, nil); got.Body.String() != "1" {
		t.Fatal(got.Body.String())
	}
	// A trust file replaced in place must change the namespace and the transport.
	if err := os.WriteFile(ca, append(append([]byte(nil), pemBytes...), pemBytes...), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(cfg); err != nil {
		t.Fatal(err)
	}
	if got := fetch(s, target, nil); got.Body.String() != "2" {
		t.Fatalf("changed CA contents reused old cache: %q", got.Body.String())
	}
	if err := os.WriteFile(ca, []byte("invalid roots"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(cfg); err == nil {
		t.Fatal("invalid trust replaced active transport")
	}
	if got := fetch(s, target+"?fresh", nil); got.Body.String() != "3" {
		t.Fatalf("failed CA reload broke old transport: %q", got.Body.String())
	}
	if s.Stats().RuntimeVersion != 2 {
		t.Fatal("failed trust reload advanced version")
	}
}

func TestReloadStopsOldCheckerBeforeDownloadTransport(t *testing.T) {
	probeEntered, probeStopped, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	closed := make(chan string, 8)
	heldPeer := make(chan string, 1)
	oldOrigin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			close(probeEntered)
			<-r.Context().Done()
			close(probeStopped)
			return
		}
		heldPeer <- r.RemoteAddr
		w.Header().Set("Cache-Control", "no-store")
		io.WriteString(w, "a")
		http.NewResponseController(w).Flush()
		select {
		case <-release:
			io.WriteString(w, "b")
		case <-r.Context().Done():
		}
	}))
	oldOrigin.Config.ConnState = func(c net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			closed <- c.RemoteAddr().String()
		}
	}
	oldOrigin.Start()
	defer oldOrigin.Close()
	newOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "new") }))
	defer newOrigin.Close()
	cfg := config.GetDefaultConfig()
	cfg.HealthCheckInterval = 1
	cfg.HealthCheckTimeout = 60
	s := lifecycleServer(t, oldOrigin.URL, cfg)
	front := httptest.NewServer(s)
	defer front.Close()
	select {
	case <-probeEntered:
	case <-time.After(time.Second):
		t.Fatal("checker did not start")
	}
	response, err := front.Client().Get(front.URL + "/held")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	prefix := make([]byte, 1)
	if _, err := io.ReadFull(response.Body, prefix); err != nil {
		t.Fatal(err)
	}
	peer := <-heldPeer
	next := *cfg
	next.UpstreamTargets = []string{newOrigin.URL}
	next.HealthCheckInterval = 0
	if err := s.Reload(&next); err != nil {
		t.Fatal(err)
	}
	select {
	case <-probeStopped:
	case <-time.After(time.Second):
		t.Fatal("retired checker survived publication")
	}
	select {
	case got := <-closed:
		if got == peer {
			t.Fatal("old business transport closed before body drained")
		}
	default:
	}
	close(release)
	body, err := io.ReadAll(response.Body)
	if err != nil || string(body) != "b" {
		t.Fatalf("retired body=%q %v", body, err)
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-closed:
			if got == peer {
				return
			}
		case <-timer.C:
			t.Fatal("retired business connection remained open")
		}
	}
}

func TestEmbeddedServerAcceptsEmptyConfigWithConstructedProxy(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "configured origin") }))
	defer origin.Close()
	p, err := proxy.NewHTTPProxy([]string{origin.URL}, "round-robin")
	if err != nil {
		t.Fatal(err)
	}
	s := server.New(&config.Config{}, nil, p)
	defer s.Shutdown()
	got := fetch(s, "http://front.test/object", http.Header{"Authorization": {"private"}})
	if got.Code != 200 || got.Body.String() != "configured origin" {
		t.Fatalf("empty embed config: %d %q", got.Code, got.Body.String())
	}
	if err := s.Reload(&config.Config{}); err == nil {
		t.Fatal("reload accepted absent routes")
	}
}

func TestReloadSharedFillOutlivesCreatorCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	oldOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(entered)
		w.Header().Set("Cache-Control", "public,max-age=3600")
		<-release
		io.WriteString(w, "old-complete")
	}))
	defer oldOrigin.Close()
	newOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public,max-age=3600")
		io.WriteString(w, "new")
	}))
	defer newOrigin.Close()
	cfg := config.GetDefaultConfig()
	s := lifecycleServer(t, oldOrigin.URL, cfg)
	frontend := httptest.NewServer(s)
	defer frontend.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", frontend.URL+"/shared", nil)
	creator := make(chan error, 1)
	go func() {
		resp, err := frontend.Client().Do(request)
		if resp != nil {
			resp.Body.Close()
		}
		creator <- err
	}()
	<-entered
	type result struct {
		body string
		err  error
	}
	follower := make(chan result, 1)
	go func() {
		resp, err := frontend.Client().Get(frontend.URL + "/shared")
		if err != nil {
			follower <- result{err: err}
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		follower <- result{string(body), err}
	}()
	waitRuntime(t, s, func(st server.Stats) bool { return st.SharedFillWaiters == 2 })
	cancel()
	if err := <-creator; !errors.Is(err, context.Canceled) {
		t.Fatalf("creator cancel: %v", err)
	}
	waitRuntime(t, s, func(st server.Stats) bool { return st.InFlight == 1 })
	next := *cfg
	next.UpstreamTargets = []string{newOrigin.URL}
	next.MaxCacheObjectBytes = 4
	if err := s.Reload(&next); err != nil {
		t.Fatal(err)
	}
	resp, err := frontend.Client().Get(frontend.URL + "/shared")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || string(body) != "new" {
		t.Fatalf("new runtime: %q %v", body, err)
	}
	close(release)
	got := <-follower
	if got.err != nil || got.body != "old-complete" {
		t.Fatalf("old follower: %q %v", got.body, got.err)
	}
	waitRuntime(t, s, func(st server.Stats) bool { return st.RetiredRuntimes == 0 && st.SharedFillTasks == 0 })
	if calls.Load() != 1 || s.Stats().Origin.Requests != 2 {
		t.Fatalf("producer lost ownership: calls=%d stats=%+v", calls.Load(), s.Stats())
	}
}

func waitRuntime(t *testing.T, s *server.Server, ready func(server.Stats) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := s.Stats()
		if ready(st) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("runtime did not drain: %+v", st)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestConcurrentReloadSerializesOneLiveTransition(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	oldOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; io.WriteString(w, "old") }))
	defer oldOrigin.Close()
	cfg := config.GetDefaultConfig()
	s := lifecycleServer(t, oldOrigin.URL, cfg)
	old := make(chan *httptest.ResponseRecorder, 1)
	go func() { old <- fetch(s, "http://front.test/held", nil) }()
	<-entered
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() { <-start; results <- s.Reload(cfg) }()
	}
	close(start)
	successes, busy := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, server.ErrReloadBusy) {
			busy++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || busy != 1 || s.Stats().RuntimeVersion != 2 {
		t.Fatalf("concurrent reload: successes=%d busy=%d stats=%+v", successes, busy, s.Stats())
	}
	close(release)
	if got := <-old; got.Body.String() != "old" {
		t.Fatal(got.Body.String())
	}
}

func TestReloadAndPurgeFenceCancelledCreatorsSharedFill(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Cache-Control", "public,max-age=3600")
		if n == 1 {
			close(entered)
			<-release
			io.WriteString(w, "old")
			return
		}
		io.WriteString(w, "fresh")
	}))
	defer origin.Close()
	cfg := config.GetDefaultConfig()
	cfg.PurgeToken = "test-reload-token"
	s := lifecycleServer(t, origin.URL, cfg)
	front := httptest.NewServer(s)
	defer front.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", front.URL+"/object", nil)
	creator := make(chan error, 1)
	go func() {
		response, err := front.Client().Do(request)
		if response != nil {
			response.Body.Close()
		}
		creator <- err
	}()
	<-entered
	follower := make(chan int, 1)
	go func() {
		response, err := front.Client().Get(front.URL + "/object")
		if err != nil {
			follower <- 0
			return
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		follower <- response.StatusCode
	}()
	waitRuntime(t, s, func(st server.Stats) bool { return st.SharedFillWaiters == 2 })
	cancel()
	if err := <-creator; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	next := *cfg
	next.CacheWriteTimeout++
	if err := s.Reload(&next); err != nil {
		t.Fatal(err)
	}
	request, _ = http.NewRequest("PURGE", front.URL+"/object", nil)
	request.Header.Set("Authorization", "Bearer "+cfg.PurgeToken)
	response, err := front.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	getFresh := func() {
		t.Helper()
		response, err := front.Client().Get(front.URL + "/object")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(body) != "fresh" {
			t.Fatalf("new generation: %q %v", body, err)
		}
	}
	getFresh()
	close(release)
	if status := <-follower; status != http.StatusServiceUnavailable {
		t.Fatalf("old shared result escaped PURGE: %d", status)
	}
	waitRuntime(t, s, func(st server.Stats) bool { return st.RetiredRuntimes == 0 && st.SharedFillTasks == 0 })
	getFresh()
	if calls.Load() != 2 {
		t.Fatalf("stale fill replaced new cache: origins=%d", calls.Load())
	}
}

func TestReloadRejectsRestartOnlySettings(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer origin.Close()
	cfg := config.GetDefaultConfig()
	cfg.PurgeToken = "original-reload-token"
	s := lifecycleServer(t, origin.URL, cfg)
	changes := map[string]func(*config.Config){
		"purge authorization":     func(c *config.Config) { c.PurgeToken = "changed" },
		"listener port":           func(c *config.Config) { c.ServerPort++ },
		"client connections":      func(c *config.Config) { c.MaxClientConnections++ },
		"listener header timeout": func(c *config.Config) { c.RequestHeaderTimeout++ },
		"listener idle timeout":   func(c *config.Config) { c.KeepAliveTimeout++ },
		"storage location":        func(c *config.Config) { c.ConfigDir = "changed" },
		"log directory":           func(c *config.Config) { c.LogfileDir = "changed" },
		"access log":              func(c *config.Config) { c.AccessLogFilename = "changed" },
		"error log":               func(c *config.Config) { c.ErrorLogFilename = "changed" },
		"listener TLS":            func(c *config.Config) { c.TLSCertFile = "cert.pem"; c.TLSKeyFile = "key.pem" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			candidate := *cfg
			change(&candidate)
			if err := s.Reload(&candidate); err == nil || !strings.Contains(err.Error(), "restart") {
				t.Fatalf("unsupported change: %v", err)
			}
		})
	}
	if got := purge(s, "http://front.test/object", "changed"); got.Code != http.StatusForbidden {
		t.Fatal("rejected reload changed management authorization")
	}
	if got := purge(s, "http://front.test/object", cfg.PurgeToken); got.Code != http.StatusOK {
		t.Fatal("original management authorization stopped working")
	}
	if s.Stats().RuntimeVersion != 1 {
		t.Fatal("restart-only edit changed current version")
	}
}

func TestReloadInvalidUpstreamPortKeepsVersionAndOrigin(t *testing.T) {
	var calls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprint(w, "original origin")
	}))
	defer origin.Close()
	cfg := config.GetDefaultConfig()
	s := lifecycleServer(t, origin.URL, cfg)
	front := httptest.NewServer(s)
	defer front.Close()
	for _, port := range []string{"0", "65536", "18446744073709551616"} {
		t.Run(port, func(t *testing.T) {
			before := s.Stats().RuntimeVersion
			candidate := *cfg
			candidate.UpstreamTargets = []string{"http://127.0.0.1:" + port}
			if err := s.Reload(&candidate); err == nil {
				t.Fatal("invalid upstream replaced running configuration")
			}
			if got := s.Stats().RuntimeVersion; got != before {
				t.Fatalf("rejected candidate advanced version %d -> %d", before, got)
			}
			response, err := front.Client().Get(front.URL + "/still-serving")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != http.StatusOK || string(body) != "original origin" {
				t.Fatalf("old service lost: status=%d body=%q error=%v", response.StatusCode, body, err)
			}
		})
	}
	if calls.Load() != 3 {
		t.Fatalf("old origin calls=%d, want 3", calls.Load())
	}
}
