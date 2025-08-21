package bakemono

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"time"

	"golang.org/x/sync/singleflight"
)

type Engine struct {
	// TODO support multi Volumes
	Volume *Vol
	Proxy  *HTTPProxy

	group singleflight.Group
}

func (e *Engine) Init(cfg *Config) error {
	opts, err := NewDefaultVolOptions(cfg.Path, cfg.SizeMb*1<<20, cfg.AvgChunkSize)
	if err != nil {
		return err
	}
	v := &Vol{}
	corrupted, err := v.Init(opts)
	if err != nil {
		return err
	}
	if corrupted {
		log.Printf("vol is corrupted, but fixed. ignore this if first time running.")
	}
	e.Volume = v

	httpProxy, err := NewHTTPProxy(cfg.Upstream.ProxyPass, cfg.Upstream.BalanceMode)
	if err != nil {
		return err
	}
	if cfg.HealthCheck {
		httpProxy.HealthCheck(cfg.HealthCheckInterval)
	}
	e.Proxy = httpProxy
	return nil
}

func (e *Engine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if err := recover(); err != nil {
			log.Printf("engine causes panic :%s", err)
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(err.(error).Error()))
		}
	}()

	cacheKey := GetCacheKey(r)
	hit, value, err := e.Volume.Get([]byte(cacheKey))
	if err != nil {
		log.Printf("cache get error: %v", err)
		// w.WriteHeader(http.StatusInternalServerError)
		// return
	}
	if hit {
		w.WriteHeader(http.StatusOK)
		w.Write(value)
		return
	}

	log.Printf("[ServeHTTP] cache miss, start singleflight for key: %s", cacheKey)

	type response struct {
		body       bytes.Buffer
		statusCode int
	}
	ch := e.group.DoChan(cacheKey, func() (interface{}, error) {
		log.Printf("[ServeHTTP] singleflight Do running for key: %s", cacheKey)
		resp, err := e.Proxy.Proxy(r)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		var res response
		res.statusCode = resp.StatusCode
		n, err := res.body.ReadFrom(resp.Body)
		if err != nil {
			return nil, err
		}

		if res.statusCode == http.StatusOK && n > 0 {
			e.Volume.Set([]byte(GetCacheKey(r)), res.body.Bytes())
		}
		return res, nil
	})

	select {
	case <-time.After(30 * time.Second):
		log.Printf("[ServeHTTP] singleflight timeout, key: %s", cacheKey)
		e.group.Forget(cacheKey)
		// TODO retry after timeout
	case res := <-ch:
		if res.Err != nil {
			log.Printf("[ServeHTTP] singleflight fetch error, key: %s, err: %v", cacheKey, res.Err)
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(res.Err.Error()))
			return
		}
		if res.Val != nil {
			resp := res.Val.(response)
			log.Printf("[ServeHTTP] singleflight fetch success, shared: %v, key: %s, size: %d", res.Shared, cacheKey, len(resp.body.Bytes()))
			w.WriteHeader(resp.statusCode)
			w.Write(resp.body.Bytes())
		}
	}
}

// stream write
func (e *Engine) copyResponse(dst http.ResponseWriter, src io.Reader) {

}

func (e *Engine) proxyAndCache(w http.ResponseWriter, r *http.Request) *respWarper {
	rw := &respWarper{ResponseWriter: w}

	e.Proxy.ServeHTTP(rw, r)

	go func() {
		if rw.cacheable() {
			log.Printf("[ServeHTTP] proxyAndCache success, set cache for key: %s, size: %d", GetCacheKey(r), rw.buf.Len())
			if err := e.Volume.Set([]byte(GetCacheKey(r)), rw.buf.Bytes()); err != nil {
				log.Printf("cache set error: %v", err)
			}
		}
	}()
	return rw
}

type respWarper struct {
	http.ResponseWriter
	// copy from ResponseWriter
	buf    bytes.Buffer
	status int
}

func (w *respWarper) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *respWarper) Write(p []byte) (int, error) {
	w.buf.Write(p)
	return w.ResponseWriter.Write(p)
}

func (w *respWarper) cacheable() bool {
	return w.status == http.StatusOK && w.buf.Len() > 0
}

// func (e *Engine) Set(key, value []byte) error {
// 	return nil
// }

// func (e *Engine) Get(key []byte) ([]byte, error) {
// 	return nil, nil
// }

// func (e *Engine) Delete(key []byte) error {
// 	return nil
// }
