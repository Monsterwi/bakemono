package server

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Monsterwi/razor/cache"
	"github.com/Monsterwi/razor/logger"
	"github.com/Monsterwi/razor/proxy"
)

func (s *Server) handler(w http.ResponseWriter, r *http.Request) {
	key := []byte(proxy.GetCacheKey(r))

	switch r.Method {
	case "PURGE":
		s.handlePurge(w, r, key)
	case "GET":
		s.handleGet(key, w, r)
	default:
		logger.Errorf("Unsupported method: %s", r.Method)
		http.Error(w, "Unsupported Method: "+r.Method, http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleGet(key []byte, w http.ResponseWriter, r *http.Request) {
	// 1. Try OpenRead (Checks RAM, OpenDir/RWW, Disk)
	vc, err := s.cache.OpenRead(key)

	if err == nil {
		// Ensure VC is closed on all exit paths
		defer vc.Close()

		// HIT (or RWW HIT)
		if vc.IsRWW() {
			logger.Infof("Cache HIT (RWW): %s", string(key))
			logger.SetCacheStatus(w, "HIT_RWW")

			// Wait for headers from Leader
			if err := vc.WaitForHeaders(5 * time.Second); err != nil {
				logger.Warnf("RWW wait failed: %v", err)
				s.proxyWithoutCache(w, r)
				return
			}

			// Load info (should be ready now)
			info, err := vc.LoadHTTPInfo()
			if err != nil {
				logger.Warnf("RWW LoadHTTPInfo failed: %v", err)
				s.proxyWithoutCache(w, r)
				return
			}

			s.serveCacheHit(w, r, vc, info)
			return
		} else {
			// Potential Normal HIT
			// We don't know if it's a real hit until we load the info (check disk)

			info, err := vc.LoadHTTPInfo()
			if err == nil {
				// Real HIT
				logger.Infof("Cache HIT: %s", string(key))
				logger.SetCacheStatus(w, "HIT")
				s.serveCacheHit(w, r, vc, info)
				return
			}

			// Fake HIT (Miss)
			// Close the reader and fall through to write
			logger.Debugf("OpenRead success but LoadHTTPInfo failed (Cache Miss): %v", err)
			// VC will be closed by defer
		}
	}

	// 2. MISS - Try OpenWrite (Become Leader)
	vc, err = s.cache.OpenWrite(key, 0)
	if err != nil {
		// Failed to write (Lock collision?), Proxy only
		logger.Warnf("OpenWrite failed (conflict?): %v", err)
		s.proxyWithoutCache(w, r)
		return
	}

	// Ensure VC is closed on all exit paths
	defer vc.Close()

	// Leader
	logger.Infof("Cache MISS (Leader): %s", string(key))
	logger.SetCacheStatus(w, "MISS")
	s.serveCacheMissLeader(w, r, vc)
}

func (s *Server) handlePurge(w http.ResponseWriter, r *http.Request, key []byte) {
	err := s.cache.Remove(key)
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

func (s *Server) serveCacheHit(w http.ResponseWriter, r *http.Request, vc *cache.CacheVC, info *cache.CacheHTTPInfo) {
	// Note: VC.Close() is handled by caller (handleGet) via defer
	// This ensures cleanup even on early returns or panics

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
}

func (s *Server) serveCacheMissLeader(w http.ResponseWriter, r *http.Request, vc *cache.CacheVC) {
	// Note: VC.Close() is handled by caller (handleGet) via defer
	// This ensures cleanup even on early returns or panics

	// Proxy to Upstream
	resp, err := s.proxy.Proxy(r)
	if err != nil {
		logger.Errorf("Upstream proxy error for key %s: %v", string(vc.Key), err)
		http.Error(w, "Upstream Error: "+err.Error(), http.StatusBadGateway)
		// VC Close will happen, but we haven't written anything.
		// Ideally we should mark it as aborted so readers get error.
		// For now, Writer closed with 0 bytes will look like EOF to readers.
		return
	}
	defer resp.RawBody().Close()

	logger.Debugf("Upstream response: Status=%d, Headers=%v", resp.StatusCode(), resp.Header())

	// Save Metadata
	info := cache.NewCacheHTTPInfo()
	info.Status = resp.StatusCode()
	// if info.Status == 0 {
	// 	info.Status = 200 // Default fallback, though Proxy should set it
	// }
	info.RequestTime = time.Now()
	info.ResponseTime = time.Now()
	info.RequestMethod = r.Method
	info.RequestURL = r.URL.String()

	for k, v := range resp.Header() {
		for _, vv := range v {
			info.ResponseHeaders.Add(k, vv)
		}
	}

	// This notifies waiting followers (headers ready)
	vc.SetHTTPInfo(info)

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
				// Client disconnected, stop writing to client but continue caching?
				// For simplicity, we abort.
				return
			}

			// Write to Cache
			// This broadcasts to followers via Cond
			if _, cErr := vc.Write(buf[:n]); cErr != nil {
				logger.Errorf("Cache write error: %v", cErr)
				// Stop caching
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
}

func (s *Server) proxyWithoutCache(w http.ResponseWriter, r *http.Request) {
	resp, err := s.proxy.Proxy(r)
	if err != nil {
		http.Error(w, "Upstream Error", http.StatusBadGateway)
		return
	}
	defer resp.RawBody().Close()

	for k, v := range resp.Header() {
		for _, vv := range v {
			w.Header().Add(k, vv)
		}
	}
	w.WriteHeader(resp.StatusCode())
	io.Copy(w, resp.RawBody())
}
