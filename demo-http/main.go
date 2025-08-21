package main

import (
	"log"
	"net/http"
	"strconv"

	"github.com/bocchi-the-cache/bakemono"
	"github.com/minio/mux"
)

func maxAllowedMiddleware(n uint) mux.MiddlewareFunc {
	sem := make(chan struct{}, n)
	acquire := func() { sem <- struct{}{} }
	release := func() { <-sem }

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			acquire()
			defer release()
			next.ServeHTTP(w, r)
		})
	}
}

func main() {
	config, err := bakemono.ReadConfig("config.yaml")
	if err != nil {
		log.Fatalf("read config error: %s", err)
	}

	err = config.Validation()
	if err != nil {
		log.Fatalf("verify config error: %s", err)
	}

	engine := &bakemono.Engine{}
	err = engine.Init(config)
	if err != nil {
		log.Fatalf("init engine error: %s", err)
	}

	router := mux.NewRouter()
	for _, l := range config.Location {
		if err != nil {
			log.Fatalf("create proxy error: %s", err)
		}
		router.PathPrefix(l.Pattern).Handler(engine)
	}

	if config.MaxAllowed > 0 {
		router.Use(maxAllowedMiddleware(config.MaxAllowed))
	}
	svr := http.Server{
		Addr:    ":" + strconv.Itoa(config.Port),
		Handler: router,
	}

	// print config detail
	config.Print()

	// listen and serve
	switch config.Schema {
	case "http":
		err := svr.ListenAndServe()
		if err != nil {
			log.Fatalf("listen and serve error: %s", err)
		}
	case "https":
		err := svr.ListenAndServeTLS(config.SSLCertificate, config.SSLCertificateKey)
		if err != nil {
			log.Fatalf("listen and serve error: %s", err)
		}
	}
}
