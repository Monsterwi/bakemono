package server

import (
	"crypto/sha256"
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
			logger.SetCacheStatus(w, "HIT")
			s.serveCacheHit(w, r, vc, info)
			return
		}
		vc.Close()
	}
	logger.SetCacheStatus(w, "MISS")
	s.serveCacheMiss(w, r, key)
}

func (s *Server) handlePurge(key []byte, w http.ResponseWriter, r *http.Request) {
	purgedKeys := s.cache.Purge(key)
	logger.SetCacheStatus(w, "PURGE")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "PURGED %s\n", strings.Join(purgedKeys, ", "))
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
	if err := streamResponse(w, vc, nil, r); err != nil {
		logger.Errorf("Cache response transfer failed: %v", err)
		panic(http.ErrAbortHandler)
	}
}

func (s *Server) serveCacheMiss(w http.ResponseWriter, r *http.Request, key []byte) {
	requested := time.Now()
	resp, err := s.proxy.Proxy(r)
	if err != nil {
		http.Error(w, "Upstream Error", http.StatusBadGateway)
		return
	}
	defer resp.RawBody().Close()
	info := cache.NewCacheHTTPInfo()
	info.RequestTime = requested
	info.ResponseTime = time.Now()
	info.RequestMethod = r.Method
	info.RequestURL = r.URL.String()
	info.Status = resp.StatusCode()
	info.ResponseHeaders = resp.Header().Clone()
	var vc *cache.CacheVC
	if _, ok := responseAge(info, time.Now()); ok && len(resp.RawResponse.Trailer) == 0 {
		vc, err = s.cache.OpenWrite(key, 0)
		if err == nil {
			vc.SetHTTPInfo(info)
			// Only the successful EOF path below may commit.
			defer vc.Abort(errors.New("cache fill incomplete"))
		}
	}
	copyHeaders(w.Header(), resp.Header())
	w.WriteHeader(resp.StatusCode())
	if err = streamResponse(w, resp.RawBody(), vc, r); err != nil {
		panic(http.ErrAbortHandler)
	}
	if err = resp.RawBody().Close(); err != nil {
		panic(http.ErrAbortHandler)
	}
	if r.Context().Err() != nil || len(resp.RawResponse.Trailer) != 0 {
		return
	}
	if vc != nil {
		if err = vc.Close(); err != nil {
			logger.Errorf("Cache commit failed: %v", err)
		}
	}
}

func streamResponse(w http.ResponseWriter, body io.Reader, vc *cache.CacheVC, r *http.Request) error {
	buf := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if err := r.Context().Err(); err != nil {
				return err
			}
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
			if vc != nil {
				if _, err := vc.Write(buf[:n]); err != nil {
					vc.Abort(err)
					// Cache failure must not interrupt an otherwise usable origin response.
					vc = nil
				}
			}
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
	logger.SetCacheStatus(w, "BYPASS")
	resp, err := s.proxy.Proxy(r)
	if err != nil {
		http.Error(w, "Upstream Error", http.StatusBadGateway)
		return
	}
	defer resp.RawBody().Close()
	copyHeaders(w.Header(), resp.Header())
	w.WriteHeader(resp.StatusCode())
	if r.Method != http.MethodHead {
		if err := streamResponse(w, resp.RawBody(), nil, r); err != nil {
			panic(http.ErrAbortHandler)
		}
	}
}
