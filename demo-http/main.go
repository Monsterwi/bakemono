package main

import (
	"log"
	"net/http"
	"runtime"
	"strconv"

	"github.com/bocchi-the-cache/bakemono"
	"github.com/gin-gonic/gin"

	_ "net/http/pprof"
)

func main() {
	runtime.SetMutexProfileFraction(1)
	runtime.SetBlockProfileRate(1)

	config, err := bakemono.ReadConfig("config.yaml")
	if err != nil {
		log.Fatalf("read config error: %s", err)
	}

	err = config.Validation()
	if err != nil {
		log.Fatalf("verify config error: %s", err)
	}
	config.Print()

	// init logger
	logger, err := bakemono.InitLogger(config.ZapConfig)
	if err != nil {
		log.Fatalf("init logger error: %s", err)
	}
	logger.Info("logger initialized")

	// init engine
	engine := &bakemono.Engine{}
	err = engine.Init(config)
	if err != nil {
		logger.Fatalf("init engine error: %s", err)
	}

	// init http server
	router := gin.New()
	router.Use(gin.Recovery())
	router.Use(bakemono.GetGinLog(config.AccessLogPath))

	router.GET("/ramcache_stats", func(c *gin.Context) {
		stats := make(map[string]interface{})
		for p, v := range engine.Volumes {
			var totalHits, totalMisses int64
			for _, s := range v.Stripes {
				st := s.RamCache.Stats()
				totalHits += int64(st.Hits)
				totalMisses += int64(st.Misses)
			}
			stats[p] = map[string]int64{
				"hits":   totalHits,
				"misses": totalMisses,
			}
		}
		c.IndentedJSON(http.StatusOK, stats)
	})

	for _, l := range config.Location {
		pattern := l.Pattern
		if pattern == "/" {
			router.NoRoute(engine.ServeHTTP)
		} else {
			if pattern[len(pattern)-1] == '/' {
				pattern = pattern[:len(pattern)-1]
			}
			router.GET(pattern+"/*path", engine.ServeHTTP)
		}
	}

	// pprof
	go http.ListenAndServe(":8080", nil)

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
