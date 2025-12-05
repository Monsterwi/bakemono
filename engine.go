package bakemono

import (
	"bytes"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/lafikl/consistent"
	"golang.org/x/sync/singleflight"
)

type Engine struct {
	Volumes map[string]*Vol
	ch      *consistent.Consistent

	Proxy *HTTPProxy
	group singleflight.Group
}

func (e *Engine) Init(cfg *Config) error {
	e.Volumes = make(map[string]*Vol)
	e.ch = consistent.New()

	var totalSize uint64
	for _, storage := range cfg.Storage {
		totalSize += storage.SizeMb
	}
	for _, storage := range cfg.Storage {
		factor := float64(storage.SizeMb) / float64(totalSize)
		opts, err := NewDefaultVolOptions(storage.Path, storage.SizeMb*1<<20, cfg.AvgChunkSize, uint64(float64(cfg.RamCacheSizeMb)*factor))
		if err != nil {
			return err
		}
		v := &Vol{}
		corrupted, err := v.Init(opts)
		if err != nil {
			return err
		}
		if corrupted {
			logger.Warn("vol is corrupted, but fixed. ignore this if first time running.")
		}
		e.Volumes[storage.Path] = v
		e.ch.Add(storage.Path)
	}

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

func (e *Engine) ServeHTTP(c *gin.Context) {
	cacheKey := GetCacheKey(c.Request)
	volumePath, err := e.ch.Get(cacheKey)
	if err != nil {
		logger.Errorf("%s hash to volume %v", cacheKey, err)
		c.String(http.StatusInternalServerError, "internal server error")
		return
	}
	volume := e.Volumes[volumePath]
	hit, reader, err := volume.Get([]byte(cacheKey))
	if err != nil {
		logger.Errorf("get cache %s, err: %v", cacheKey, err)
	}

	if hit {
		c.Set("CACHE_STATUS", "HIT")
		logger.Debugf("cache hit, return from cache %s", cacheKey)
		c.Header("Content-Type", "application/octet-stream")
		http.ServeContent(c.Writer, c.Request, cacheKey, time.Now(), reader)
		return
	}

	c.Set("CACHE_STATUS", "MISS")

	logger.Debugf("cache miss, fetch from upstream %s", cacheKey)

	type response struct {
		body       bytes.Buffer
		statusCode int
	}
	ch := e.group.DoChan(cacheKey, func() (interface{}, error) {
		proxyResp, err := e.Proxy.Proxy(c.Request)
		if err != nil {
			return nil, err
		}
		defer proxyResp.RawResponse.Body.Close()

		var res response
		res.statusCode = proxyResp.StatusCode()
		n, err := res.body.ReadFrom(proxyResp.RawResponse.Body)
		if err != nil {
			return nil, err
		}

		if res.statusCode == http.StatusOK && n > 0 {
			volumePath, err := e.ch.Get(GetCacheKey(c.Request))
			if err != nil {
				logger.Errorf("consistent hash get error for set %s, err: %v", GetCacheKey(c.Request), err)
				return nil, err
			}
			volume := e.Volumes[volumePath]
			err = volume.Set([]byte(GetCacheKey(c.Request)), res.body.Bytes())
			if err != nil {
				logger.Errorf("volume set error %s, err: %v", GetCacheKey(c.Request), err)
				return nil, err
			}
		}
		return res, nil
	})

	select {
	case <-time.After(30 * time.Second):
		logger.Errorf("singleflight timeout %s", cacheKey)
		e.group.Forget(cacheKey)
		// TODO retry after timeout
	case res := <-ch:
		if res.Err != nil {
			logger.Errorf("singleflight fetch error %s, err: %v", cacheKey, res.Err)
			c.String(http.StatusBadGateway, res.Err.Error())
			return
		}
		if res.Val != nil {
			resp := res.Val.(response)
			logger.Debugf("fetch from upstream success %s, shared: %v", cacheKey, res.Shared)
			c.Data(resp.statusCode, "application/octet-stream", resp.body.Bytes())
		}
	}
}
