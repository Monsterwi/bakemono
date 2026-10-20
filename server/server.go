package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/Monsterwi/razor/cache"
	appconfig "github.com/Monsterwi/razor/config"
	"github.com/Monsterwi/razor/logger"
	"github.com/Monsterwi/razor/proxy"
	"golang.org/x/net/netutil"
)

type Server struct {
	config               *appconfig.Config
	startupFiles         [32]byte
	cache                *cache.Cache
	current, retired     *runtimeSnapshot
	closedOrigin         proxy.Stats
	reloadMu             sync.Mutex
	httpServer           *http.Server
	tlsConfig            *tls.Config
	counters             counters
	fills                *fillGroup
	ctx                  context.Context
	cancel               context.CancelFunc
	mu                   sync.Mutex
	draining             bool
	handlers             sync.WaitGroup
	shutdown             sync.Once
	shutdownErr, initErr error
	backgroundStop       chan struct{}
	background           sync.WaitGroup
	now                  func() time.Time
}

// Option configures a server before it starts serving requests.
type Option func(*Server)

// WithClock supplies the clock used for HTTP age and freshness decisions.
func WithClock(now func() time.Time) Option {
	return func(s *Server) {
		if now != nil {
			s.now = now
		}
	}
}

func New(cfg *appconfig.Config, c *cache.Cache, p *proxy.HTTPProxy, options ...Option) *Server {
	if cfg == nil {
		cfg = appconfig.GetDefaultConfig()
	}
	cfg = cfg.WithDefaults()
	s := &Server{config: cfg, cache: c, backgroundStop: make(chan struct{}), now: time.Now}
	for _, option := range options {
		option(s)
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.fills = &fillGroup{ctx: s.ctx, cache: c, tasks: make(map[string]*fillTask)}
	s.startupFiles, s.initErr = runtimeFiles(cfg)
	if s.initErr == nil {
		s.current, s.initErr = newRuntime(cfg, p, 1)
	}
	if s.initErr == nil {
		s.initErr = s.configureNetwork()
	}
	if s.initErr == nil {
		s.initErr = s.current.startHealth()
	}
	if s.initErr != nil {
		p.Close()
	}

	return s
}

// Serve exposes the real listener lifecycle to embedders and integration tests.
func (s *Server) Serve(listener net.Listener) error {
	if s.initErr != nil {
		listener.Close()
		return s.initErr
	}
	s.mu.Lock()
	if s.draining || s.httpServer != nil {
		s.mu.Unlock()
		listener.Close()
		return fmt.Errorf("server already started or draining")
	}
	logged := logger.AccessLogMiddleware(filepath.Join(s.config.LogfileDir, s.config.AccessLogFilename))(s)
	hs := &http.Server{TLSConfig: s.tlsConfig, Handler: logged, ReadHeaderTimeout: time.Duration(s.config.RequestHeaderTimeout) * time.Second, IdleTimeout: time.Duration(s.config.KeepAliveTimeout) * time.Second, MaxHeaderBytes: 64 << 10, BaseContext: func(net.Listener) context.Context { return s.ctx }}
	s.httpServer = hs
	s.background.Add(1)
	go func() {
		defer s.background.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.backgroundStop:
				return
			case <-ticker.C:
				logger.Infof("HTTP resource stats: %+v", s.Stats())
			}
		}
	}()
	s.mu.Unlock()
	listener = netutil.LimitListener(listener, s.config.MaxClientConnections)
	if s.tlsConfig != nil {
		return hs.ServeTLS(listener, "", "")
	}
	return hs.Serve(listener)
}

func (s *Server) Run() error {
	if s.initErr != nil {
		return errors.Join(s.initErr, s.Shutdown())
	}
	listener, err := net.Listen("tcp", ":"+strconv.Itoa(s.config.ServerPort))
	if err != nil {
		return errors.Join(err, s.Shutdown())
	}
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quit)
	result := make(chan error, 1)
	go func() { result <- s.Serve(listener) }()
	select {
	case <-quit:
		return s.Shutdown()
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		return errors.Join(err, s.Shutdown())
	}
}
func (s *Server) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(s.shutdownTimeout())*time.Second)
	defer cancel()
	return s.ShutdownContext(ctx)
}

// ShutdownContext rejects new work, drains then cancels, and joins all handlers
// before closing storage. Kernel disk I/O may outlive the grace period; waiting
// for it is required to avoid writes against a closed cache file.
func (s *Server) ShutdownContext(ctx context.Context) error {
	s.shutdown.Do(func() {
		s.reloadMu.Lock()
		s.mu.Lock()
		s.draining = true
		hs := s.httpServer
		current, retired := s.current, s.retired
		close(s.backgroundStop)
		s.mu.Unlock()
		s.reloadMu.Unlock()
		if current != nil {
			current.proxy.StopBackground()
		}
		if retired != nil {
			retired.proxy.StopBackground()
		}
		s.background.Wait()
		var err error
		if hs != nil {
			err = hs.Shutdown(ctx)
			if err != nil {
				s.cancel()
				err = errors.Join(err, hs.Close())
			}
		}
		done := make(chan struct{})
		go func() { s.handlers.Wait(); close(done) }()
		select {
		case <-done:
		case <-ctx.Done():
			err = errors.Join(err, ctx.Err())
			s.cancel()
			if hs != nil {
				err = errors.Join(err, hs.Close())
			}
			<-done
		}
		s.cancel()
		s.fills.producers.Wait()
		s.mu.Lock()
		s.closeRetiredLocked()
		if s.current != nil {
			err = errors.Join(err, s.current.proxy.Close())
		}
		s.mu.Unlock()
		if s.cache != nil {
			err = errors.Join(err, s.cache.Close())
		}
		s.shutdownErr = err
	})
	return s.shutdownErr
}

func (s *Server) shutdownTimeout() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil {
		return s.current.config.ShutdownTimeout
	}
	return s.config.ShutdownTimeout
}
