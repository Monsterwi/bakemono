package main

import (
	"log"
	"strconv"

	"github.com/bocchi-the-cache/bakemono"
	"github.com/gin-gonic/gin"
)

func main() {
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
	for _, l := range config.Location {
		if err != nil {
			logger.Fatalf("create proxy error: %s", err)
		}
		router.GET(l.Pattern+"/*path", engine.ServeHTTP)
	}

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
