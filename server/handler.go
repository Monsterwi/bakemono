package server

import (
	"context"
	"crypto/subtle"
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
	runtime := requestRuntime(r)
	key := []byte(fmt.Sprintf("%s:%s", runtime.namespace, runtime.proxy.CacheKey(r)))
	switch r.Method {
	case "PURGE":
		s.handlePurge(key, w, r)
	case http.MethodGet, http.MethodHead:
		s.handleGet(key, w, r)
	default:
		http.Error(w, "Unsupported Method", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleGet(key []byte, w http.ResponseWriter, r *http.Request) {
	if !requestCacheable(r) {
		s.proxyWithoutCache(w, r)
		return
	}
	token, _ := s.cache.BeginFill(key, key)
	var selected *cache.CacheVC
	var selectedInfo *cache.CacheHTTPInfo
	// shortcut: bound negotiation fanout; excess variants safely bypass caching.
	keys, keysErr := s.cache.KeysForSelectorLimit(key, maxHTTPVariants)
	if keysErr != nil {
		s.proxyWithoutCache(w, r)
		return
	}
	for _, candidate := range keys {
		vc, err := s.cache.OpenReadCommitted([]byte(candidate))
		if err != nil {
			continue
		}
		info, err := vc.LoadHTTPInfo()
		if err != nil {
			vc.Close()
			continue
		}
		fields, valid := varyFields(info.ResponseHeaders)
		if _, safe := responsePolicy(info, s.now()); valid && safe && string(representationKey(key, r.Header, fields)) == candidate {
			selected, selectedInfo = vc, info
			break
		}
		vc.Close()
	}
	if selected != nil {
		defer selected.Close()
		if requestFresh(r, selectedInfo, s.now()) {
			s.cacheStatus(w, "HIT")
			s.serveCacheHit(w, r, selected, selectedInfo)
			return
		}
	}
	if r.Method == http.MethodHead && selected == nil {
		s.proxyWithoutCache(w, r)
		return
	}
	s.cacheStatus(w, "MISS")
	s.serveCacheMiss(w, r, key, token, selected, selectedInfo)
}

func (s *Server) handlePurge(key []byte, w http.ResponseWriter, r *http.Request) {
	if requestRuntime(r).config.PurgeToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+requestRuntime(r).config.PurgeToken)) != 1 {
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
	if policy, ok := responsePolicy(info, s.now()); ok {
		age := policy.age
		w.Header().Set("Age", strconv.FormatInt(int64(age/time.Second), 10))
	}
	if clientNotModified(r, info.ResponseHeaders) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(info.Status)
	if r.Method == http.MethodHead {
		return
	}
	if err := s.streamResponse(w, vc, nil, r); err != nil {
		logger.Errorf("Cache response transfer failed: %v", err)
		panic(http.ErrAbortHandler)
	}
}

func (s *Server) serveCacheMiss(w http.ResponseWriter, r *http.Request, key []byte, token *cache.FillToken, stored *cache.CacheVC, previous *cache.CacheHTTPInfo) {
	originRequest := r
	if stored != nil {
		originRequest = r.Clone(r.Context())
		originRequest.Header.Del("If-None-Match")
		originRequest.Header.Del("If-Modified-Since")
		if tag := previous.ResponseHeaders.Get("ETag"); validETag(tag) {
			originRequest.Header.Set("If-None-Match", tag)
		} else if modified := previous.ResponseHeaders.Get("Last-Modified"); modified != "" {
			originRequest.Header.Set("If-Modified-Since", modified)
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		requested := s.now()
		resp, originHeaders, err := requestRuntime(r).proxy.ProxyWithOriginHeaders(originRequest)
		if err != nil {
			s.upstreamError(w, r, err)
			return
		}
		defer resp.RawBody().Close()
		if attempt > 0 && resp.StatusCode() == http.StatusNotModified {
			http.Error(w, "Unusable origin validation", http.StatusBadGateway)
			return
		}
		info := cache.NewCacheHTTPInfo()
		info.RequestTime, info.ResponseTime = requested, s.now()
		info.RequestMethod, info.RequestURL, info.Status = r.Method, r.URL.String(), resp.StatusCode()
		info.ResponseHeaders = originHeaders
		if stored != nil && resp.StatusCode() == http.StatusNotModified {
			merged, valid := mergeValidation(previous, info, originRequest)
			resp.RawBody().Close()
			if valid && sameVary(previous.ResponseHeaders, merged.ResponseHeaders) {
				merged.ResponseHeaders = mergeResponseHeaders(previous.ResponseHeaders, resp.Header())
				ctx, cancel := context.WithTimeout(r.Context(), time.Duration(requestRuntime(r).config.CacheWriteTimeout)*time.Second)
				release, acquireErr := requestRuntime(r).writers.Acquire(ctx, time.Duration(requestRuntime(r).config.QueueTimeout)*time.Second)
				updateErr := acquireErr
				if acquireErr == nil {
					updateErr = s.cache.UpdateHTTPInfo(ctx, token, stored, merged)
					release()
				}
				cancel()
				if updateErr == nil {
					s.serveCacheHit(w, r, stored, merged)
					return
				}
			} else if token != nil {
				_ = s.cache.InvalidateFill(token)
			}
			// An unusable 304 cannot stand in for a representation. Retry once without
			// validators, preserving the original token so PURGE cannot be bypassed.
			stored = nil
			originRequest = r.Clone(r.Context())
			originRequest.Header.Del("If-None-Match")
			originRequest.Header.Del("If-Modified-Since")
			continue
		}
		_, cacheable := responsePolicy(info, s.now())
		fields, _ := varyFields(originHeaders)
		representation := representationKey(key, r.Header, fields)
		info.ResponseHeaders = resp.Header().Clone()
		keys, keysErr := s.cache.KeysForSelectorLimit(key, maxHTTPVariants)
		if keysErr != nil {
			cacheable = false
			token = nil
		}
		var remove []string
		for _, old := range keys {
			if !sameRepresentationSchema(key, []byte(old), representation) {
				remove = append(remove, old)
			}
		}
		if !cacheable || r.Method == http.MethodHead {
			if token != nil {
				_ = s.cache.InvalidateFill(token)
			}
		}
		var fill *cacheFill
		canAdd := len(keys) < maxHTTPVariants || containsField(keys, string(representation)) || len(remove) > 0
		if token != nil && cacheable && canAdd && r.Method == http.MethodGet && len(resp.RawResponse.Trailer) == 0 && s.cacheSizeAllowed(r, representation, info, resp.RawResponse.ContentLength) {
			release, acquireErr := requestRuntime(r).writers.Acquire(r.Context(), time.Duration(requestRuntime(r).config.QueueTimeout)*time.Second)
			if acquireErr == nil {
				cacheCtx, cacheCancel := context.WithCancel(r.Context())
				defer cacheCancel()
				vc, writeErr := s.cache.OpenWriteFillKeyReplacing(cacheCtx, token, representation, remove)
				if writeErr == nil {
					vc.SetHTTPInfo(info)
					fill = &cacheFill{release: release, vc: vc, cancel: cacheCancel, timeout: time.Duration(requestRuntime(r).config.CacheWriteTimeout) * time.Second, limit: int64(requestRuntime(r).config.MaxCacheObjectBytes), stats: &s.counters}
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
		if resp.StatusCode() == http.StatusOK && clientNotModified(r, resp.Header()) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.WriteHeader(resp.StatusCode())
		if r.Method != http.MethodHead {
			if err = s.streamResponse(w, resp.RawBody(), fill, r); err != nil {
				panic(http.ErrAbortHandler)
			}
		}
		if err = resp.RawBody().Close(); err != nil {
			panic(http.ErrAbortHandler)
		}
		if r.Context().Err() != nil || len(resp.RawResponse.Trailer) != 0 {
			return
		}
		fill.commit()
		return
	}
	http.Error(w, "Unusable origin validation", http.StatusBadGateway)
}

func (s *Server) streamResponse(w http.ResponseWriter, body io.Reader, fill *cacheFill, r *http.Request) error {
	buf := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if err := r.Context().Err(); err != nil {
				return err
			}
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Duration(requestRuntime(r).config.TransactionTimeoutIn) * time.Second))
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
	resp, err := requestRuntime(r).proxy.Proxy(r)
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
func (s *Server) cacheSizeAllowed(r *http.Request, key []byte, info *cache.CacheHTTPInfo, length int64) bool {
	if length > int64(requestRuntime(r).config.MaxCacheObjectBytes) || len(key) > cache.MaxIndexKeyBytes {
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
