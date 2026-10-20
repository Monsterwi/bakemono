package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Monsterwi/razor/cache"
	"github.com/Monsterwi/razor/logger"
	"github.com/Monsterwi/razor/proxy"
)

func (s *Server) handler(w http.ResponseWriter, r *http.Request) {
	// Route changes must not reuse objects populated by a previous origin policy.
	route, _ := json.Marshal(struct {
		Targets   []string
		Algorithm string
	}{s.config.UpstreamTargets, s.config.UpstreamAlgo})
	namespace := sha256.Sum256(route)
	key := []byte(fmt.Sprintf("%x:%s", namespace, s.proxy.CacheKey(r)))
	switch r.Method {
	case "PURGE":
		s.handlePurge(key, w, r)
	case http.MethodGet:
		s.handleGet(key, w, r)
	case http.MethodHead:
		s.proxyWithoutCache(w, r)
	default:
		http.Error(w, "Unsupported Method", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleGet(key []byte, w http.ResponseWriter, r *http.Request) {
	if !requestCacheable(r) {
		s.proxyWithoutCache(w, r)
		return
	}
	// shortcut: serve committed objects only until privacy-safe request collapsing exists.
	if vc, err := s.cache.OpenReadCommitted(key); err == nil {
		info, loadErr := vc.LoadHTTPInfo()
		_, fresh := responseAge(info, time.Now())
		if loadErr == nil && fresh {
			defer vc.Close()
			s.cacheStatus(w, "HIT")
			s.serveCacheHit(w, r, vc, info)
			return
		}
		vc.Close()
	}
	s.cacheStatus(w, "MISS")
	s.serveCacheMiss(w, r, key)
}

func (s *Server) handlePurge(key []byte, w http.ResponseWriter, r *http.Request) {
	if s.config.PurgeToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.config.PurgeToken)) != 1 {
		logger.SetLogField(w, "purge_authorized", false)
		http.Error(w, "PURGE forbidden", http.StatusForbidden)
		return
	}
	logger.SetLogField(w, "purge_authorized", true)
	purgedKeys, err := s.cache.PurgeSelector(key, strings.HasSuffix(r.URL.EscapedPath(), "/") && r.URL.RawQuery == "")
	if err != nil {
		logger.SetLogField(w, "purge_result", "unavailable")
		http.Error(w, "PURGE unavailable", http.StatusServiceUnavailable)
		return
	}
	logger.SetLogField(w, "purge_count", len(purgedKeys))
	logger.SetLogField(w, "purge_result", "complete")
	s.cacheStatus(w, "PURGE")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintln(w, "PURGED")
}

func copyHeaders(dst, src http.Header) {
	for k, values := range src {
		for _, v := range values {
			dst.Add(k, v)
		}
	}
}

func (s *Server) serveCacheHit(w http.ResponseWriter, r *http.Request, vc *cache.CacheVC, info *cache.CacheHTTPInfo) {
	copyHeaders(w.Header(), info.ResponseHeaders)
	if age, ok := responseAge(info, time.Now()); ok {
		w.Header().Set("Age", strconv.FormatInt(int64(age/time.Second), 10))
	}
	w.WriteHeader(info.Status)
	if err := s.streamResponse(w, vc, nil, r); err != nil {
		logger.Errorf("Cache response transfer failed: %v", err)
		panic(http.ErrAbortHandler)
	}
}

func (s *Server) serveCacheMiss(w http.ResponseWriter, r *http.Request, key []byte) {
	requested := time.Now()
	token, tokenErr := s.cache.BeginFill(key, key)
	resp, originHeaders, err := s.proxy.ProxyWithOriginHeaders(r)
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	defer resp.RawBody().Close()
	info := cache.NewCacheHTTPInfo()
	info.RequestTime = requested
	info.ResponseTime = time.Now()
	info.RequestMethod = r.Method
	info.RequestURL = r.URL.String()
	info.Status = resp.StatusCode()
	info.ResponseHeaders = originHeaders
	_, cacheable := responseAge(info, time.Now())
	info.ResponseHeaders = resp.Header().Clone()
	var fill *cacheFill
	if tokenErr == nil && cacheable && len(resp.RawResponse.Trailer) == 0 && s.cacheSizeAllowed(key, info, resp.RawResponse.ContentLength) {
		release, acquireErr := s.writers.Acquire(r.Context(), time.Duration(s.config.QueueTimeout)*time.Second)
		if acquireErr == nil {
			cacheCtx, cacheCancel := context.WithCancel(r.Context())
			defer cacheCancel()
			vc, err := s.cache.OpenWriteFill(cacheCtx, token, 0)
			if err == nil {
				vc.SetHTTPInfo(info)
				// Only the successful EOF path below may commit.
				fill = &cacheFill{release: release, vc: vc, cancel: cacheCancel, timeout: time.Duration(s.config.CacheWriteTimeout) * time.Second, limit: int64(s.config.MaxCacheObjectBytes), stats: &s.counters}
				defer fill.abort(errors.New("cache fill incomplete"))
			} else {
				release()
				s.counters.cacheSkipped.Add(1)
			}
		} else {
			s.counters.cacheSkipped.Add(1)
		}
	} else {
		s.counters.cacheSkipped.Add(1)
	}
	copyHeaders(w.Header(), resp.Header())
	w.WriteHeader(resp.StatusCode())
	if err = s.streamResponse(w, resp.RawBody(), fill, r); err != nil {
		panic(http.ErrAbortHandler)
	}
	if err = resp.RawBody().Close(); err != nil {
		panic(http.ErrAbortHandler)
	}
	if r.Context().Err() != nil || len(resp.RawResponse.Trailer) != 0 {
		return
	}
	fill.commit()
}

func (s *Server) streamResponse(w http.ResponseWriter, body io.Reader, fill *cacheFill, r *http.Request) error {
	buf := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if err := r.Context().Err(); err != nil {
				return err
			}
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Duration(s.config.TransactionTimeoutIn) * time.Second))
			written, err := w.Write(buf[:n])
			if err != nil {
				return err
			}
			if written != n {
				return io.ErrShortWrite
			}
			if err := http.NewResponseController(w).Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
				return err
			}
			fill.write(buf[:n])
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (s *Server) proxyWithoutCache(w http.ResponseWriter, r *http.Request) {
	s.cacheStatus(w, "BYPASS")
	resp, err := s.proxy.Proxy(r)
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	defer resp.RawBody().Close()
	copyHeaders(w.Header(), resp.Header())
	w.WriteHeader(resp.StatusCode())
	if r.Method != http.MethodHead {
		if err := s.streamResponse(w, resp.RawBody(), nil, r); err != nil {
			panic(http.ErrAbortHandler)
		}
	}
}

func (s *Server) upstreamError(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusBadGateway
	if errors.Is(err, proxy.ErrOverloaded) {
		status = http.StatusServiceUnavailable
		s.counters.rejected.Add(1)
	}
	if timeout, ok := err.(interface{ Timeout() bool }); ok && timeout.Timeout() {
		status = http.StatusGatewayTimeout
	}
	http.Error(w, "Upstream unavailable", status)
}
func (s *Server) cacheSizeAllowed(key []byte, info *cache.CacheHTTPInfo, length int64) bool {
	if length > int64(s.config.MaxCacheObjectBytes) || len(key) > cache.MaxIndexKeyBytes {
		return false
	}
	size := len(info.RequestURL)
	for k, values := range info.ResponseHeaders {
		size += len(k)
		for _, v := range values {
			size += len(v)
		}
	}
	return size <= 64<<10
}
