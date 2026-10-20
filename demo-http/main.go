package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/net/netutil"

	"github.com/Monsterwi/razor/cache"
	appconfig "github.com/Monsterwi/razor/config"
	"github.com/Monsterwi/razor/logger"
	"github.com/Monsterwi/razor/proxy"
	"github.com/Monsterwi/razor/server"

	_ "net/http/pprof"
)

func main() {
	runtime.SetMutexProfileFraction(1)
	runtime.SetBlockProfileRate(1)

	config, err := readConfig("config.yaml")
	if err != nil {
		log.Fatalf("read config error: %s", err)
	}

	var listenerTLS *tls.Config
	if config.Schema == "https" {
		certificate, err := tls.LoadX509KeyPair(config.SSLCertificate, config.SSLCertificateKey)
		if err != nil {
			log.Fatalf("load listener TLS certificate: %v", err)
		}
		listenerTLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	}
	for _, path := range append(config.ZapConfig.OutputPaths, config.ZapConfig.ErrorOutputPaths...) {
		if path != "stdout" && path != "stderr" {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				log.Fatal(err)
			}
		}
	}
	zapLogger, err := config.ZapConfig.Build()
	if err != nil {
		log.Fatal(err)
	}
	defer zapLogger.Sync()
	logger.Logger = zapLogger.Sugar()

	engine := &cache.Cache{Scheme: "http"}
	for _, span := range config.Storage {
		engine.Stripes = append(engine.Stripes, cache.NewStripe(span.Path, span.SizeMB*1024*1024, 0, cache.WithRamCache(config.RAMCacheSizeMB*1024*1024)))
	}
	if err := engine.Init(); err != nil {
		log.Fatal(err)
	}
	httpProxy, err := proxy.NewHTTPProxy(config.Upstream.ProxyPass, config.Upstream.BalanceMode, proxy.WithCAFile(config.UpstreamCAFile))
	if err != nil {
		log.Fatal(err)
	}
	limits := appconfig.GetDefaultConfig()
	limits.UpstreamTargets = config.Upstream.ProxyPass
	limits.UpstreamAlgo = config.Upstream.BalanceMode
	handler := server.New(limits, engine, httpProxy)
	defer handler.Shutdown()
	if config.TCPHealthCheck {
		if err := httpProxy.StartHealthChecks(proxy.HealthConfig{Interval: time.Duration(config.HealthCheckInterval) * time.Second, Timeout: 3 * time.Second, Failures: 2, Successes: 2}); err != nil {
			log.Fatal(err)
		}
	}
	cachedHandler := logger.AccessLogMiddleware(config.AccessLogPath)(handler)

	// init http server
	router := http.NewServeMux()

	router.HandleFunc("GET /ramcache_stats", func(w http.ResponseWriter, r *http.Request) {
		stats := make(map[string]interface{})
		for _, stripe := range engine.Stripes {
			stats[stripe.Path] = map[string]uint64{"hits": 0, "misses": 0}
			if stripe.RamCache != nil {
				st := stripe.RamCache.Stats()
				stats[stripe.Path] = map[string]uint64{"hits": st.Hits, "misses": st.Misses}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "    ")
		_ = encoder.Encode(stats)
	})

	for _, l := range config.Location {
		pattern := l.Pattern
		if pattern == "/" {
			router.Handle("/", cachedHandler)
		} else {
			if pattern[len(pattern)-1] == '/' {
				pattern = pattern[:len(pattern)-1]
			}
			router.Handle(pattern+"/", cachedHandler)
		}
	}

	// Embedders own listener timeouts; the shared handler owns work and cache drain.
	listener, err := net.Listen("tcp", ":"+strconv.Itoa(config.Port))
	if err != nil {
		log.Fatal(err)
	}
	listener = netutil.LimitListener(listener, limits.MaxClientConnections)

	httpServer := &http.Server{TLSConfig: listenerTLS, Handler: router, ReadHeaderTimeout: time.Duration(limits.RequestHeaderTimeout) * time.Second, IdleTimeout: time.Duration(limits.KeepAliveTimeout) * time.Second, MaxHeaderBytes: 64 << 10}
	profiling := &http.Server{Addr: "127.0.0.1:8080", ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := profiling.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Errorf("Profiling listener: %v", err)
		}
	}()
	defer profiling.Close()
	quit, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	result := make(chan error, 1)
	go func() {
		if config.Schema == "https" {
			result <- httpServer.ServeTLS(listener, "", "")
		} else {
			result <- httpServer.Serve(listener)
		}
	}()
	select {
	case <-quit.Done():
	case err = <-result:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	httpProxy.StopBackground()
	grace, cancel := context.WithTimeout(context.Background(), time.Duration(limits.ShutdownTimeout)*time.Second)
	defer cancel()
	if shutdownErr := httpServer.Shutdown(grace); shutdownErr != nil {
		err = errors.Join(err, shutdownErr, httpServer.Close())
	}
	err = errors.Join(err, handler.ShutdownContext(grace))
	if err != nil {
		logger.Fatalf("Demo shutdown: %v", err)
	}
}
