package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/Monsterwi/razor/cache"
	appconfig "github.com/Monsterwi/razor/config"
	"github.com/Monsterwi/razor/logger"
	"github.com/Monsterwi/razor/proxy"
	"github.com/Monsterwi/razor/server"
	"github.com/gin-gonic/gin"

	_ "net/http/pprof"
)

func main() {
	runtime.SetMutexProfileFraction(1)
	runtime.SetBlockProfileRate(1)

	config, err := readConfig("config.yaml")
	if err != nil {
		log.Fatalf("read config error: %s", err)
	}

	for _, path := range append(config.ZapConfig.OutputPaths, config.ZapConfig.ErrorOutputPaths...) {
		if path != "stdout" && path != "stderr" {
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				log.Fatal(err)
			}
		}
	}
	zapLogger, err := config.ZapConfig.Build()
	if err != nil {
		log.Fatal(err)
	}
	defer zapLogger.Sync()
	logger.Logger = zapLogger.Sugar()

	engine := &cache.Cache{Scheme: "http"}
	for _, span := range config.Storage {
		engine.Stripes = append(engine.Stripes, cache.NewStripe(span.Path, span.SizeMB*1024*1024, 0, cache.WithRamCache(config.RAMCacheSizeMB*1024*1024)))
	}
	if err := engine.Init(); err != nil {
		log.Fatal(err)
	}
	defer engine.Close()
	httpProxy, err := proxy.NewHTTPProxy(config.Upstream.ProxyPass, config.Upstream.BalanceMode)
	if err != nil {
		log.Fatal(err)
	}
	if config.TCPHealthCheck {
		httpProxy.HealthCheck(config.HealthCheckInterval)
	}
	handler := server.New(&appconfig.Config{}, engine, httpProxy)
	cachedHandler := logger.AccessLogMiddleware(config.AccessLogPath)(handler)

	// init http server
	router := gin.New()
	router.Use(gin.Recovery())

	router.GET("/ramcache_stats", func(c *gin.Context) {
		stats := make(map[string]interface{})
		for _, stripe := range engine.Stripes {
			stats[stripe.Path] = map[string]uint64{"hits": 0, "misses": 0}
			if stripe.RamCache != nil {
				st := stripe.RamCache.Stats()
				stats[stripe.Path] = map[string]uint64{"hits": st.Hits, "misses": st.Misses}
			}
		}
		c.IndentedJSON(http.StatusOK, stats)
	})

	for _, l := range config.Location {
		pattern := l.Pattern
		if pattern == "/" {
			router.NoRoute(gin.WrapH(cachedHandler))
		} else {
			if pattern[len(pattern)-1] == '/' {
				pattern = pattern[:len(pattern)-1]
			}
			router.GET(pattern+"/*path", gin.WrapH(cachedHandler))
		}
	}

	// pprof
	go http.ListenAndServe("127.0.0.1:8080", nil)

	switch config.Schema {
	case "http":
		err := router.Run(":" + strconv.Itoa(config.Port))
		if err != nil {
			logger.Fatalf("listen and serve error: %s", err)
		}
	case "https":
		err := router.RunTLS(":"+strconv.Itoa(config.Port), config.SSLCertificate, config.SSLCertificateKey)
		if err != nil {
			logger.Fatalf("listen and serve error: %s", err)
		}
	}
}
