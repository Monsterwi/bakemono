package bakemono

import (
	"bytes"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/singleflight"
)

type Engine struct {
	// TODO support multi Volumes
	Volume *Vol
	Proxy  *HTTPProxy

	group singleflight.Group
}

func (e *Engine) Init(cfg *Config) error {
	opts, err := NewDefaultVolOptions(cfg.Path, cfg.SizeMb*1<<20, cfg.AvgChunkSize, cfg.RamCacheEntries)
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

func (e *Engine) ServeHTTP(c *gin.Context) {
	cacheKey := GetCacheKey(c.Request)
	hit, value, err := e.Volume.Get([]byte(cacheKey))
	if err != nil {
		logger.Errorf("cache get error %s, err: %v", cacheKey, err)
	}

	// set cache status for access log
	if hit {
		c.Set("CACHE_STATUS", "HIT")
	} else {
		c.Set("CACHE_STATUS", "MISS")
	}

	if hit {
		logger.Debugf("cache hit, return from cache %s", cacheKey)
		c.Data(http.StatusOK, "application/octet-stream", value)
		return
	}

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
			e.Volume.Set([]byte(GetCacheKey(c.Request)), res.body.Bytes())
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
