package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Monsterwi/razor/cache"
	"github.com/Monsterwi/razor/logger"
)

// Global instances
var (
	globalCache *cache.Cache
	httpProxy   *HTTPProxy
)

func main() {
	// 1. Load configuration
	cfg := GetDefaultConfig()

	var err error
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

	// 4. Setup graceful shutdown
	server := &http.Server{
		Addr:    cfg.ServerAddr,
		Handler: http.HandlerFunc(handler),
	}

	// Start server in goroutine
	go func() {
		logger.Infof("Server started on %s", cfg.ServerAddr)
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

// NewCache builds and initializes cache stripes from the provided config.
func NewCache(cfg *Config) (*cache.Cache, error) {
	store, err := LoadStore(cfg.StorageConfigPath)
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

	// 1. Try Read Cache
	vc, err := globalCache.OpenRead(key)
	if err == nil && vc != nil {
		// Verify cache hit by trying to load metadata
		// If LoadHTTPInfo returns io.EOF, it means key doesn't exist (Cache Miss)
		info, infoErr := vc.LoadHTTPInfo()
		if infoErr == nil && info != nil {
			// Cache Hit
			// log.Printf("Cache HIT: %s", key)
			serveCacheHit(w, vc, info)
			return
		}
		// If infoErr is io.EOF, treat as Cache Miss
		// log.Printf("Cache MISS (no metadata): %s, err: %v", key, infoErr)
		// Close the VC if it's not usable
		_ = vc.Close()
	}

	// 2. Cache Miss
	logger.Infof("Cache MISS: %s", string(key))
	serveCacheMiss(w, r, key)
}

func serveCacheHit(w http.ResponseWriter, vc *cache.CacheVC, info *cache.CacheHTTPInfo) {
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

// ---- Adapting Upstream.go code ----
// (Assuming Upstream structs are available or copied here)
// ...
