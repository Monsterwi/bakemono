package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	appconfig "github.com/Monsterwi/razor/config"
	"github.com/Monsterwi/razor/internal/budget"
	"github.com/Monsterwi/razor/proxy"
)

var ErrReloadBusy = errors.New("reload busy: retired runtime still has active requests")

// A request and each independent producer retain one immutable runtime until all
// body I/O ends. At most current + one retired runtime can consume resources.
type runtimeSnapshot struct {
	config             *appconfig.Config
	proxy              *proxy.HTTPProxy
	admission, writers *budget.Limiter
	namespace          string
	version            uint64
	refs               int // guarded by Server.mu
}

type runtimeContextKey struct{}

func requestRuntime(r *http.Request) *runtimeSnapshot {
	return r.Context().Value(runtimeContextKey{}).(*runtimeSnapshot)
}

func withRequestRuntime(r *http.Request, ctx context.Context, runtime *runtimeSnapshot) *http.Request {
	return r.WithContext(context.WithValue(ctx, runtimeContextKey{}, runtime))
}

func newRuntime(cfg *appconfig.Config, p *proxy.HTTPProxy, version uint64) (*runtimeSnapshot, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := p.ConfigureLimits(proxy.Limits{Requests: cfg.MaxOriginRequests, Queued: cfg.MaxOriginQueuedRequests, Connections: cfg.MaxOriginConnections, QueueTimeout: time.Duration(cfg.QueueTimeout) * time.Second, DialTimeout: time.Duration(cfg.ConnectTimeout) * time.Second, TLSHandshakeTimeout: time.Duration(cfg.TLSHandshakeTimeout) * time.Second, ResponseHeaderTimeout: time.Duration(cfg.ResponseHeaderTimeout) * time.Second, BodyIdleTimeout: time.Duration(cfg.TransactionTimeoutOut) * time.Second}); err != nil {
		return nil, err
	}
	rt := &runtimeSnapshot{config: cfg, proxy: p, version: version, namespace: p.CacheNamespace(), admission: budget.New(cfg.MaxRequests, cfg.MaxQueuedRequests)}
	rt.writers = budget.New(min(cfg.MaxCacheWriters, cfg.CacheWriteBufferBytes/cfg.CacheWriterReservation()), cfg.MaxQueuedCacheWriters)
	return rt, nil
}

func (rt *runtimeSnapshot) startHealth() error {
	cfg := rt.config
	return rt.proxy.StartHealthChecks(proxy.HealthConfig{Interval: time.Duration(cfg.HealthCheckInterval) * time.Second, Timeout: time.Duration(cfg.HealthCheckTimeout) * time.Second, Failures: cfg.HealthCheckFailures, Successes: cfg.HealthCheckSuccesses, Path: cfg.HealthCheckPath})
}

func (s *Server) retainRuntime(rt *runtimeSnapshot) func() {
	s.mu.Lock()
	rt.refs++
	s.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { s.releaseRuntime(rt) }) }
}
func (s *Server) releaseRuntime(rt *runtimeSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt.refs--
	s.closeRetiredLocked()
}
func (s *Server) closeRetiredLocked() {
	if s.retired != nil && s.retired.refs == 0 {
		s.retired.proxy.Close()
		addOriginStats(&s.closedOrigin, s.retired.proxy.Stats())
		s.retired = nil
	}
}

// Reload validates and builds before publication. Concurrent calls serialize;
// a second transition is rejected while the preceding runtime still has leases.
func (s *Server) Reload(cfg *appconfig.Config) error {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	if cfg == nil {
		return fmt.Errorf("reload config is nil")
	}
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	if s.draining || s.initErr != nil {
		s.mu.Unlock()
		return fmt.Errorf("server unavailable")
	}
	if s.retired != nil {
		s.mu.Unlock()
		return ErrReloadBusy
	}
	old := s.current
	s.mu.Unlock()
	if !old.proxy.Reloadable() {
		return fmt.Errorf("custom upstream transport requires restart")
	}
	if restartSettings(cfg) != restartSettings(s.config) {
		return fmt.Errorf("listener, storage or logging changes require restart")
	}
	files, err := runtimeFiles(cfg)
	if err != nil {
		return fmt.Errorf("reload files: %w", err)
	}
	if files != s.startupFiles {
		return fmt.Errorf("storage or listener TLS contents changed; restart required")
	}
	p, err := proxy.NewHTTPProxy(cfg.UpstreamTargets, cfg.UpstreamAlgo, proxy.WithTrustedProxies(cfg.TrustedProxies), proxy.WithCAFile(cfg.UpstreamCAFile))
	if err != nil {
		return err
	}
	candidate, err := newRuntime(cfg, p, old.version+1)
	if err != nil {
		p.Close()
		return err
	}
	// All potentially failing construction precedes the atomic publication.
	if err = candidate.startHealth(); err != nil {
		p.Close()
		return err
	}
	s.mu.Lock()
	s.current, s.retired = candidate, old
	s.mu.Unlock()
	old.proxy.StopBackground()
	s.mu.Lock()
	s.closeRetiredLocked()
	s.mu.Unlock()
	return nil
}

type restartConfig struct {
	configDir, logDir, accessLog, errorLog, cert, key string
	port, connections, headerTimeout, idleTimeout     int
}

func restartSettings(c *appconfig.Config) restartConfig {
	return restartConfig{c.ConfigDir, c.LogfileDir, c.AccessLogFilename, c.ErrorLogFilename, c.TLSCertFile, c.TLSKeyFile, c.ServerPort, c.MaxClientConnections, c.RequestHeaderTimeout, c.KeepAliveTimeout}
}

// File content is part of restart compatibility; paths alone miss in-place edits.
func runtimeFiles(c *appconfig.Config) ([32]byte, error) {
	var contents [][]byte
	for i, path := range []string{filepath.Join(c.ConfigDir, appconfig.StorageConfigFilename), c.TLSCertFile, c.TLSKeyFile} {
		if path == "" {
			contents = append(contents, nil)
			continue
		}
		data, err := os.ReadFile(path)
		// Embedders may create storage directly without a storage.config file.
		if i == 0 && os.IsNotExist(err) {
			data = nil
			err = nil
		}
		if err != nil {
			return [32]byte{}, err
		}
		contents = append(contents, data)
	}
	data, _ := json.Marshal(contents)
	return sha256.Sum256(data), nil
}
