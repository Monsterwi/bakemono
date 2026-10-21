package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Monsterwi/razor/cache"
	"github.com/Monsterwi/razor/proxy"
)

const rangeBlockSize int64 = 1 << 20

var errRangeBypass = errors.New("range representation unsupported")
var errRangeChanged = errors.New("range representation changed")
var errRangeUnavailable = errors.New("range origin unavailable")

type rangeManifest struct {
	Version                            int
	BlockSize                          int64
	Total                              int64
	ETag, Origin, Variant, Incarnation string
}

func rangeEligible(r *http.Request) bool {
	if r.Method != http.MethodGet || len(r.Header.Values("Range")) != 1 || !strings.HasPrefix(r.Header.Get("Range"), "bytes=") || strings.Contains(r.Header.Get("Range"), ",") {
		return false
	}
	for _, name := range []string{"If-Match", "If-Unmodified-Since", "If-None-Match", "If-Modified-Since"} {
		if hasHeader(r.Header, name) {
			return false
		}
	}
	if value := r.Header.Get("Accept-Encoding"); value != "" && value != "identity" {
		return false
	}
	if connectionNominates(r.Header, "Range", "If-Range", "Accept-Encoding") {
		return false
	}
	check := r.Clone(r.Context())
	check.Header.Del("Range")
	check.Header.Del("If-Range")
	return requestCacheable(check)
}

func rangeIdentity(root []byte, origin *proxy.Origin, r *http.Request) ([]byte, string) {
	variant, _ := json.Marshal(r.Header.Values("Accept-Language"))
	hash := sha256.Sum256([]byte(origin.Identity() + "\x00" + string(variant)))
	return []byte(fmt.Sprintf("%s\nrange:manifest:%x", root, hash)), string(variant)
}
func rangeBlockKey(root []byte, m *rangeManifest, index int64) []byte {
	hash := sha256.Sum256([]byte(m.Origin + "\x00" + m.Variant + "\x00" + m.ETag + "\x00" + m.Incarnation))
	return []byte(fmt.Sprintf("%s\nrange:block:%x:%d", root, hash, index))
}

func (s *Server) readRangeManifest(key []byte, origin, variant string) (*rangeManifest, *cache.CacheHTTPInfo, error) {
	vc, err := s.cache.OpenReadCommitted(key)
	if err != nil {
		return nil, nil, err
	}
	defer vc.Close()
	info, err := vc.LoadHTTPInfo()
	if err != nil {
		return nil, nil, err
	}
	data, err := io.ReadAll(io.LimitReader(vc, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return nil, nil, errors.New("invalid range manifest")
	}
	var m rangeManifest
	if json.Unmarshal(data, &m) != nil || m.Version != 1 || m.BlockSize != rangeBlockSize || m.Total < 0 || !strongRangeTag(m.ETag) || m.Origin != origin || m.Variant != variant || len(m.Incarnation) != 32 {
		return nil, nil, errors.New("invalid range manifest")
	}
	return &m, info, nil
}
func strongRangeTag(tag string) bool { return validETag(tag) && !strings.HasPrefix(tag, "W/") }
func identityEncoding(h http.Header) bool {
	return len(h.Values("Content-Encoding")) == 0 || (len(h.Values("Content-Encoding")) == 1 && h.Get("Content-Encoding") == "identity")
}

// rangeFill shares the existing bounded coordinator; every reader opens its own
// complete committed object, and detached producers retain their own resources.
func (s *Server) rangeFill(r *http.Request, key []byte, token *cache.FillToken, produce func(*fillTask, *http.Request, fillResources) fillResult) error {
	resources := s.captureFillResources(r)
	identity := r.Clone(r.Context())
	identity.Header.Del("Range")
	identity.Header.Del("If-Range")
	task, creator, err := s.fills.join(fillIdentity(identity, key, resources.version), token, resources.config)
	if err != nil {
		resources.release()
		return err
	}
	defer s.fills.leave(task, creator)
	if creator {
		request := withRequestRuntime(r.Clone(task.ctx), task.ctx, resources.runtime)
		go func() {
			defer s.fills.finish(task)
			defer resources.release()
			release, err := resources.writers.Acquire(task.ctx, time.Duration(resources.config.QueueTimeout)*time.Second)
			result := fillResult{err: err}
			if err == nil {
				defer release()
				result = produce(task, request, resources)
			}
			if result.err == nil {
				result.err = task.ctx.Err()
			}
			if result.err == nil {
				result.err = s.cache.CheckFill(task.token)
			}
			task.publish(result)
		}()
	} else {
		resources.release()
	}
	select {
	case <-r.Context().Done():
		return r.Context().Err()
	case <-task.done:
	}
	if task.result.err != nil {
		return task.result.err
	}
	return s.cache.CheckFill(task.token)
}

func (s *Server) writeRangeObject(task *fillTask, key []byte, info *cache.CacheHTTPInfo, body io.Reader, limit int64, resources fillResources) error {
	ctx, cancel := context.WithCancel(task.ctx)
	vc, err := s.cache.OpenWriteFillKeyReplacing(ctx, task.token, key, nil)
	if err != nil {
		cancel()
		return err
	}
	vc.SetHTTPInfo(info)
	fill := &cacheFill{vc: vc, cancel: cancel, timeout: time.Duration(resources.config.CacheWriteTimeout) * time.Second, limit: limit, stats: &s.counters}
	defer fill.abort(errors.New("incomplete range object"))
	buf := make([]byte, 32<<10)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			fill.write(buf[:n])
			if fill.err != nil {
				return fill.err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if fill.written != limit {
		return io.ErrUnexpectedEOF
	}
	if err := task.ctx.Err(); err != nil {
		return err
	}
	return fill.commit()
}

func (s *Server) ensureRangeManifest(r *http.Request, root, key []byte, parent *cache.FillToken, origin *proxy.Origin, variant string) (*rangeManifest, *cache.CacheHTTPInfo, bool, error) {
	if m, info, err := s.readRangeManifest(key, origin.Identity(), variant); err == nil && requestFresh(r, info, s.now()) {
		return m, info, true, nil
	}
	token, err := s.cache.DeriveFill(parent, key)
	if err != nil {
		return nil, nil, false, err
	}
	err = s.rangeFill(r, key, token, func(task *fillTask, request *http.Request, resources fillResources) fillResult {
		if _, info, err := s.readRangeManifest(key, origin.Identity(), variant); err == nil && requestFresh(request, info, s.now()) {
			return fillResult{key: key}
		}
		if !origin.Available() {
			return fillResult{err: errRangeUnavailable}
		}
		request.Method = http.MethodHead
		request.Header.Del("Range")
		request.Header.Del("If-Range")
		request.Header.Set("Accept-Encoding", "identity")
		started := s.now()
		resp, headers, err := origin.ProxyWithOriginHeaders(request)
		if err != nil {
			return fillResult{err: err}
		}
		defer resp.RawBody().Close()
		info := cache.NewCacheHTTPInfo()
		info.RequestTime, info.ResponseTime = started, s.now()
		info.RequestMethod, info.RequestURL, info.Status = http.MethodGet, r.URL.String(), resp.StatusCode()
		info.ResponseHeaders = headers
		_, safe := responsePolicy(info, s.now())
		if !safe || resp.StatusCode() != 200 || !strongRangeTag(headers.Get("ETag")) || len(headers.Values("ETag")) != 1 || resp.RawResponse.ContentLength < 0 || !identityEncoding(headers) || len(resp.RawResponse.Trailer) != 0 {
			_ = s.cache.InvalidateFill(parent)
			return fillResult{err: errRangeBypass}
		}
		if !cacheSizeAllowed(resources.config.MaxCacheObjectBytes, key, info, resp.RawResponse.ContentLength) {
			return fillResult{err: errRangeBypass}
		}
		var incarnation [16]byte
		if _, err = rand.Read(incarnation[:]); err != nil {
			return fillResult{err: err}
		}
		m := rangeManifest{Version: 1, BlockSize: rangeBlockSize, Total: resp.RawResponse.ContentLength, ETag: headers.Get("ETag"), Origin: origin.Identity(), Variant: variant, Incarnation: hex.EncodeToString(incarnation[:])}
		encoded, err := json.Marshal(m)
		if err != nil || len(encoded) > 64<<10 {
			return fillResult{err: errRangeBypass}
		}
		info.ResponseHeaders = resp.Header().Clone()
		err = s.writeRangeObject(task, key, info, bytes.NewReader(encoded), int64(len(encoded)), resources)
		return fillResult{key: key, err: err}
	})
	if err != nil {
		return nil, nil, false, err
	}
	m, info, err := s.readRangeManifest(key, origin.Identity(), variant)
	return m, info, false, err
}

func rangeBounds(value string, total int64) (int64, int64, error) {
	value = strings.TrimPrefix(value, "bytes=")
	left, right, ok := strings.Cut(value, "-")
	if !ok {
		return 0, 0, errRangeBypass
	}
	decimal := func(value string) (int64, error) {
		if value == "" {
			return 0, errRangeBypass
		}
		for _, v := range value {
			if v < '0' || v > '9' {
				return 0, errRangeBypass
			}
		}
		return strconv.ParseInt(value, 10, 64)
	}
	if left == "" {
		suffix, err := decimal(right)
		if err != nil || suffix <= 0 || total == 0 {
			return 0, 0, errRangeBypass
		}
		return max(int64(0), total-suffix), total - 1, nil
	}
	start, err := decimal(left)
	if err != nil || start >= total {
		return 0, 0, errRangeBypass
	}
	end := total - 1
	if right != "" {
		end, err = decimal(right)
		if err != nil || end < start {
			return 0, 0, errRangeBypass
		}
		end = min(end, total-1)
	}
	return start, end, nil
}

func (s *Server) readRangeBlock(key []byte, m *rangeManifest, index int64) ([]byte, *cache.CacheHTTPInfo, error) {
	vc, err := s.cache.OpenReadCommitted(key)
	if err != nil {
		return nil, nil, err
	}
	defer vc.Close()
	info, err := vc.LoadHTTPInfo()
	if err != nil {
		return nil, nil, err
	}
	size := min(rangeBlockSize, m.Total-index*rangeBlockSize)
	if info.Status != http.StatusPartialContent || info.ResponseHeaders.Get("ETag") != m.ETag {
		return nil, nil, errRangeChanged
	}
	data, err := io.ReadAll(io.LimitReader(vc, size+1))
	if err != nil {
		return nil, nil, err
	}
	if int64(len(data)) != size {
		return nil, nil, io.ErrUnexpectedEOF
	}
	return data, info, nil
}
func (s *Server) rangeBlockFresh(r *http.Request, info *cache.CacheHTTPInfo) bool {
	normal := *info
	normal.Status = http.StatusOK
	normal.ResponseHeaders = info.ResponseHeaders.Clone()
	normal.ResponseHeaders.Del("Content-Range")
	return requestFresh(r, &normal, s.now())
}

func (s *Server) invalidateRange(root []byte, parent *cache.FillToken, m *rangeManifest) {
	hash := sha256.Sum256([]byte(m.Origin + "\x00" + m.Variant))
	key := []byte(fmt.Sprintf("%s\nrange:manifest:%x", root, hash))
	token, err := s.cache.DeriveFill(parent, key)
	if err == nil {
		current, _, err := s.readRangeManifest(key, m.Origin, m.Variant)
		if err == nil && current.Incarnation == m.Incarnation {
			_ = s.cache.InvalidateFill(token)
		}
	}
	_ = s.cache.InvalidateFill(parent)
}

func (s *Server) ensureRangeBlock(r *http.Request, root []byte, parent *cache.FillToken, m *rangeManifest, index int64, origin *proxy.Origin) (bool, error) {
	key := rangeBlockKey(root, m, index)
	if _, info, err := s.readRangeBlock(key, m, index); err == nil && s.rangeBlockFresh(r, info) {
		return true, nil
	}
	token, err := s.cache.DeriveFill(parent, key)
	if err != nil {
		return false, err
	}
	err = s.rangeFill(r, key, token, func(task *fillTask, request *http.Request, resources fillResources) fillResult {
		if _, info, err := s.readRangeBlock(key, m, index); err == nil && s.rangeBlockFresh(request, info) {
			return fillResult{key: key}
		}
		if !origin.Available() {
			return fillResult{err: errRangeUnavailable}
		}
		start := index * rangeBlockSize
		end := min(start+rangeBlockSize, m.Total) - 1
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
		request.Header.Del("If-Range")
		request.Header.Set("If-Match", m.ETag)
		request.Header.Set("Accept-Encoding", "identity")
		started := s.now()
		resp, headers, err := origin.ProxyWithOriginHeaders(request)
		if err != nil {
			return fillResult{err: err}
		}
		defer resp.RawBody().Close()
		if resp.StatusCode() != 206 || headers.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", start, end, m.Total) || len(headers.Values("Content-Range")) != 1 || headers.Get("ETag") != m.ETag || len(headers.Values("ETag")) != 1 || resp.RawResponse.ContentLength != end-start+1 || !identityEncoding(headers) || len(resp.RawResponse.Trailer) != 0 {
			s.invalidateRange(root, parent, m)
			if resp.StatusCode() == 200 {
				return fillResult{err: errRangeBypass}
			}
			return fillResult{err: errRangeChanged}
		}
		info := cache.NewCacheHTTPInfo()
		info.RequestTime, info.ResponseTime = started, s.now()
		info.RequestMethod, info.RequestURL, info.Status = http.MethodGet, r.URL.String(), 200
		info.ResponseHeaders = headers.Clone()
		info.ResponseHeaders.Del("Content-Range")
		if _, safe := responsePolicy(info, s.now()); !safe {
			s.invalidateRange(root, parent, m)
			return fillResult{err: errRangeBypass}
		}
		info.Status = 206
		info.ResponseHeaders = resp.Header().Clone()
		// Buffer one block so length/trailer validation completes before publication.
		data, err := io.ReadAll(io.LimitReader(resp.RawBody(), rangeBlockSize+1))
		if err != nil {
			return fillResult{err: err}
		}
		if int64(len(data)) != end-start+1 || len(resp.RawResponse.Trailer) != 0 {
			return fillResult{err: io.ErrUnexpectedEOF}
		}
		err = s.writeRangeObject(task, key, info, bytes.NewReader(data), int64(len(data)), resources)
		return fillResult{key: key, err: err}
	})
	return false, err
}

func (s *Server) rangeFits(root []byte, m *rangeManifest, start, end int64) bool {
	required := make(map[*cache.Stripe]int64, len(s.cache.Stripes))
	for index := start / rangeBlockSize; index <= end/rangeBlockSize; index++ {
		stripe := s.cache.KeyToStripe(rangeBlockKey(root, m, index))
		if stripe == nil {
			return false
		}
		required[stripe] += min(rangeBlockSize, m.Total-index*rangeBlockSize) + (64 << 10)
		if required[stripe]+(1<<20) > stripe.Skip+stripe.Len-stripe.Start {
			return false
		}
	}
	return true
}
func (s *Server) proxyRangeBypass(w http.ResponseWriter, r *http.Request, origin *proxy.Origin) {
	if !origin.Available() {
		s.upstreamError(w, r, errRangeUnavailable)
		return
	}
	s.cacheStatus(w, "BYPASS")
	resp, _, err := origin.ProxyWithOriginHeaders(r)
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	defer resp.RawBody().Close()
	copyHeaders(w.Header(), resp.Header())
	w.WriteHeader(resp.StatusCode())
	if err = s.streamResponse(w, resp.RawBody(), r); err != nil {
		panic(http.ErrAbortHandler)
	}
}
func (s *Server) serveRange(w http.ResponseWriter, r *http.Request, root []byte) {
	resources := s.captureFillResources(r)
	defer resources.release()
	parent, err := s.cache.BeginFill(root, root)
	if err != nil {
		s.proxyWithoutCache(w, r)
		return
	}
	var origin *proxy.Origin
	var m *rangeManifest
	var info *cache.CacheHTTPInfo
	cachedReady := false
	// Prefer a healthy source; an unhealthy source is usable only for a complete
	// fresh interval. Pin one source before sending, never stitch replicas.
	for _, candidate := range resources.proxy.ConfiguredOrigins() {
		key, variant := rangeIdentity(root, candidate, r)
		cached, metadata, readErr := s.readRangeManifest(key, candidate.Identity(), variant)
		if readErr != nil || !requestFresh(r, metadata, s.now()) {
			continue
		}
		if candidate.Available() {
			origin, m, info, cachedReady = candidate, cached, metadata, false
			break
		}
		if origin != nil {
			continue
		}
		if value := r.Header.Get("If-Range"); value != "" && (!strongRangeTag(value) || value != cached.ETag) {
			continue
		}
		start, end, boundsErr := rangeBounds(r.Header.Get("Range"), cached.Total)
		complete := true
		if boundsErr == nil {
			for index := start / rangeBlockSize; index <= end/rangeBlockSize; index++ {
				_, blockInfo, err := s.readRangeBlock(rangeBlockKey(root, cached, index), cached, index)
				if err != nil || !s.rangeBlockFresh(r, blockInfo) {
					complete = false
					break
				}
			}
		}
		if complete {
			origin, m, info, cachedReady = candidate, cached, metadata, true
		}
	}
	hit := m != nil
	if origin == nil {
		origin, err = resources.proxy.SelectOrigin(r)
		if err != nil {
			s.upstreamError(w, r, err)
			return
		}
		key, variant := rangeIdentity(root, origin, r)
		m, info, hit, err = s.ensureRangeManifest(r, root, key, parent, origin, variant)
	}
	if errors.Is(err, errRangeBypass) {
		s.proxyRangeBypass(w, r, origin)
		return
	}
	if err != nil {
		s.upstreamError(w, r, err)
		return
	}
	if value := r.Header.Get("If-Range"); value != "" && (!strongRangeTag(value) || value != m.ETag) {
		full := r.Clone(r.Context())
		full.Header.Del("Range")
		full.Header.Del("If-Range")
		s.proxyRangeBypass(w, full, origin)
		return
	}
	start, end, err := rangeBounds(r.Header.Get("Range"), m.Total)
	if err != nil {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", m.Total))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	if !s.rangeFits(root, m, start, end) {
		http.Error(w, "Range exceeds cache capacity", http.StatusServiceUnavailable)
		return
	}
	for index := start / rangeBlockSize; !cachedReady && index <= end/rangeBlockSize; index++ {
		reused, err := s.ensureRangeBlock(r, root, parent, m, index, origin)
		hit = hit && reused
		if errors.Is(err, errRangeBypass) {
			s.proxyRangeBypass(w, r, origin)
			return
		}
		if err != nil {
			s.upstreamError(w, r, err)
			return
		}
	}
	if err = s.cache.CheckFill(parent); err != nil {
		http.Error(w, "Range invalidated", http.StatusServiceUnavailable)
		return
	}
	copyHeaders(w.Header(), info.ResponseHeaders)
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, m.Total))
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	w.Header().Set("Accept-Ranges", "bytes")
	// shortcut: require downstream validation until freshness from all blocks is combined.
	w.Header().Set("Cache-Control", "no-cache")
	if policy, ok := responsePolicy(info, s.now()); ok {
		w.Header().Set("Age", strconv.FormatInt(int64(policy.age/time.Second), 10))
	}
	status := "MISS"
	if hit {
		status = "HIT"
	}
	s.cacheStatus(w, status)
	w.WriteHeader(http.StatusPartialContent)
	for index := start / rangeBlockSize; index <= end/rangeBlockSize; index++ {
		data, _, err := s.readRangeBlock(rangeBlockKey(root, m, index), m, index)
		if err != nil {
			panic(http.ErrAbortHandler)
		}
		low := max(start-index*rangeBlockSize, int64(0))
		high := min(end-index*rangeBlockSize+1, int64(len(data)))
		if err = s.streamResponse(w, bytes.NewReader(data[low:high]), r); err != nil {
			panic(http.ErrAbortHandler)
		}
	}
}
