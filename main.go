package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/Monsterwi/razor/cache"
	appconfig "github.com/Monsterwi/razor/config"
	"github.com/Monsterwi/razor/logger"
	"github.com/sevlyar/go-daemon"
)

// Global instances
var (
	globalCache *cache.Cache
	httpProxy   *HTTPProxy
)

// Command line flags
var (
	daemonMode  = flag.Bool("d", false, "Run in daemon mode")
	configDir   = flag.String("c", "conf", "Path to config directory")
	pidFile     = flag.String("p", "/var/run/bakemono.pid", "PID file")
	showHelp    = flag.Bool("h", false, "Show help message")
	showVersion = flag.Bool("v", false, "Show version information")
)

const (
	Version   = "1.0.0"
	BuildDate = "2026-01-06"
)

func main() {
	// Parse command line flags
	flag.Parse()

	// Show help
	if *showHelp {
		printHelp()
		return
	}

	// Show version
	if *showVersion {
		printVersion()
		return
	}

	// If daemon mode is requested
	if *daemonMode {
		cntxt := &daemon.Context{}
		cntxt.PidFileName = *pidFile

		d, err := cntxt.Reborn()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start daemon: %v\n", err)
			os.Exit(1)
		}

		// 父进程：释放资源并退出
		if d != nil {
			fmt.Printf("Bakemono started in background (PID: %d)\n", d.Pid)
			return
		}

		// 子进程：释放父进程资源，继续执行后续代码
		defer cntxt.Release()
	}

	// 1. Load configuration from records.config
	cfg, err := appconfig.Load(*configDir)
	if err != nil {
		// Logger not initialized yet, use fmt
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	// Initialize logger with configured log directory
	// Pass daemon mode flag to avoid writing to closed stdout/stderr
	if err := initLogger(cfg, *daemonMode); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}

	globalCache, err = NewCache(cfg)
	if err != nil {
		logger.Fatalf("Failed to start cache: %v", err)
	}
	defer globalCache.Close()

	logger.Infof("Cache initialized with %d stripes", len(globalCache.Stripes))

	// 3. Initialize Upstream
	httpProxy, err = NewHTTPProxy(cfg.UpstreamTargets, cfg.UpstreamAlgo)
	if err != nil {
		logger.Fatalf("Failed to init proxy: %v", err)
	}

	// 4. Setup access log middleware and handler
	accessLogPath := filepath.Join(cfg.LogfileDir, cfg.AccessLogFilename)
	loggedHandler := logger.AccessLogMiddleware(accessLogPath)(http.HandlerFunc(handler))

	// 5. Setup graceful shutdown
	server := &http.Server{
		Addr:    ":" + strconv.Itoa(cfg.ServerPort),
		Handler: loggedHandler,
	}

	// Start server in goroutine
	go func() {
		logger.Infof("Server started on %s", server.Addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("Server error: %v", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("Shutting down server...")

	// Create shutdown context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Shutdown HTTP server gracefully
	if err := server.Shutdown(ctx); err != nil {
		logger.Errorf("Server forced to shutdown: %v", err)
	}

	// Close cache (this will flush AggBuffer and save metadata before closing)
	logger.Info("Closing cache...")
	if err := globalCache.Close(); err != nil {
		logger.Errorf("Error closing cache: %v", err)
	} else {
		logger.Info("Cache closed successfully")
	}

	logger.Info("Server shutdown complete")
}

// printHelp displays usage information
func printHelp() {
	fmt.Printf(`Bakemono - High Performance CDN Cache Server

Usage:
  bakemono [options]

Options:
  -d          Run in daemon mode (background)
  -c string   Path to records.config file (default: "conf/records.config")
  -p string   Path to PID file (optional, recommended for daemon mode)
  -h          Show this help message
  -v          Show version information

Examples:
  # Run in foreground
  bakemono

  # Run in background
  bakemono -d

  # Run with custom config and PID file
  bakemono -d -c /etc/bakemono/records.config -p /var/run/bakemono.pid

  # Show version
  bakemono -v

For more information, visit: https://github.com/Monsterwi/bakemono
`)
}

// printVersion displays version information
func printVersion() {
	fmt.Printf("Bakemono CDN Cache Server\n")
	fmt.Printf("Version: %s\n", Version)
	fmt.Printf("Build Date: %s\n", BuildDate)
	fmt.Printf("Go Version: %s\n", runtime.Version())
}

func handlePurge(w http.ResponseWriter, r *http.Request, key []byte) {
	err := globalCache.Remove(key)
	if err != nil {
		logger.Errorf("Failed to purge key %s: %v", string(key), err)
		logger.SetCacheStatus(w, "PURGE_FAIL")
		http.Error(w, "Purge Failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	logger.Infof("PURGED: %s", string(key))
	logger.SetCacheStatus(w, "PURGE")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf("PURGED %s\n", string(key))))
}

// NewCache builds and initializes cache stripes from the provided config.
func NewCache(cfg *appconfig.Config) (*cache.Cache, error) {
	storageConfigPath := filepath.Join(cfg.ConfigDir, appconfig.StorageConfigFilename)
	store, err := appconfig.LoadStore(storageConfigPath)
	if err != nil {
		return nil, fmt.Errorf("load store: %w", err)
	}

	numStripes := len(store.Spans)
	if numStripes == 0 {
		return nil, errors.New("no spans available for cache")
	}

	logger.Infof("Configuration loaded: %d spans, will create %d stripes", len(store.Spans), numStripes)
	logger.Infof("Initializing cache with %d stripes from %d spans", numStripes, len(store.Spans))

	c, err := cache.NewCacheFromStore(store, numStripes)
	if err != nil {
		return nil, fmt.Errorf("create cache: %w", err)
	}

	if err := c.Init(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("init cache: %w", err)
	}

	return c, nil
}

func handler(w http.ResponseWriter, r *http.Request) {
	key := []byte(GetCacheKey(r))

	switch r.Method {
	case "PURGE":
		handlePurge(w, r, key)
		return
	case "GET":
		// 1. Try Read Cache
		vc, err := globalCache.OpenRead(key)
		if err == nil && vc != nil {
			// Verify cache hit by trying to load metadata
			// If LoadHTTPInfo returns io.EOF, it means key doesn't exist (Cache Miss)
			info, infoErr := vc.LoadHTTPInfo()
			if infoErr == nil && info != nil {
				// Cache Hit
				logger.SetCacheStatus(w, "HIT")
				serveCacheHit(w, r, vc, info)
				return
			}
			// If infoErr is io.EOF, treat as Cache Miss
			// Close the VC if it's not usable
			_ = vc.Close()
		}
		// 2. Cache Miss
		logger.Infof("Cache MISS: %s", string(key))
		logger.SetCacheStatus(w, "MISS")
		serveCacheMiss(w, r, key)
		return
	default:
		logger.Errorf("Unsupported method: %s", r.Method)
		http.Error(w, "Unsupported Method: "+r.Method, http.StatusMethodNotAllowed)
		return
	}
}

func serveCacheHit(w http.ResponseWriter, r *http.Request, vc *cache.CacheVC, info *cache.CacheHTTPInfo) {
	// Set Response Headers from CacheHTTPInfo
	for k, v := range info.ResponseHeaders {
		for _, vv := range v {
			w.Header().Add(k, vv)
		}
	}

	// Write Status
	w.WriteHeader(int(info.Status))

	// Copy Body
	_, err := io.Copy(w, vc)
	if err != nil {
		logger.Errorf("Error sending cache body: %v", err)
	}

	// Close the VC after reading
	_ = vc.Close()
}

func serveCacheMiss(w http.ResponseWriter, r *http.Request, key []byte) {
	// Proxy to Upstream
	resp, err := httpProxy.Proxy(r)
	if err != nil {
		logger.Errorf("Upstream proxy error for key %s: %v", string(key), err)
		http.Error(w, "Upstream Error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.RawBody().Close()

	logger.Debugf("Upstream response: Status=%d, Headers=%v", resp.StatusCode(), resp.Header())

	// Prepare Cache Writer
	vc, err := globalCache.OpenWrite(key, 0)
	if err != nil {
		logger.Warnf("Failed to open cache writer: %v", err)
		// Proceed without caching
		io.Copy(w, resp.RawBody())
		return
	}
	defer vc.Close()

	// Save Metadata
	// Convert http.Response to CacheHTTPInfo
	info := cache.NewCacheHTTPInfo()
	info.Status = resp.StatusCode()
	info.RequestTime = time.Now()
	info.ResponseTime = time.Now()
	info.RequestMethod = r.Method
	info.RequestURL = r.URL.String()

	// Copy response headers
	for k, v := range resp.Header() {
		for _, vv := range v {
			info.ResponseHeaders.Add(k, vv)
		}
	}

	vc.SetHTTPInfo(info)

	// MultiWriter: Write to Client AND Cache
	// Since vc.Write buffers/streams to cache, we can use io.MultiWriter
	// But CacheVC.Close() is needed to commit.

	// We need a wrapper that writes to w and cacheVC
	// Note: io.Copy buffer size defaults to 32KB.

	// Write Headers to Client
	for k, v := range resp.Header() {
		for _, vv := range v {
			w.Header().Add(k, vv)
		}
	}
	w.WriteHeader(resp.StatusCode())

	// Stream Body
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.RawBody().Read(buf)
		if n > 0 {
			// Write to Client
			if _, wErr := w.Write(buf[:n]); wErr != nil {
				// Client disconnected?
				// Should we continue caching?
				// ATS usually aborts caching if client aborts (unless background fill enabled)
				// Close will be called by defer
				return
			}

			// Write to Cache
			if _, cErr := vc.Write(buf[:n]); cErr != nil {
				logger.Errorf("Cache write error: %v", cErr)
				// Stop caching, but continue serving client?
				// Or just ignore cache error.
			}
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			logger.Errorf("Upstream read error: %v", readErr)
			return
		}
	}

	// Cache will be committed by defer vc.Close()
}

// initializeLogger initializes the logger with the configured log directory
func initLogger(cfg *appconfig.Config, isDaemon bool) error {
	// Initialize production logger with configured paths
	// Pass daemon mode to avoid writing to closed stdout/stderr
	_, err := logger.InitLogger(cfg.LogfileDir, cfg.ErrorLogFilename, isDaemon)
	if err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}

	logger.Infof("Logger initialized successfully (daemon mode: %v)", isDaemon)
	logger.Infof("Log directory: %s", cfg.LogfileDir)
	logger.Infof("Access log: %s/%s", cfg.LogfileDir, cfg.AccessLogFilename)
	logger.Infof("Error log: %s/%s", cfg.LogfileDir, cfg.ErrorLogFilename)

	return nil
}

// ---- Adapting Upstream.go code ----
// (Assuming Upstream structs are available or copied here)
// ...
