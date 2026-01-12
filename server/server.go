package server

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/Monsterwi/razor/cache"
	appconfig "github.com/Monsterwi/razor/config"
	"github.com/Monsterwi/razor/logger"
	"github.com/Monsterwi/razor/proxy"
)

type Server struct {
	config     *appconfig.Config
	cache      *cache.Cache
	proxy      *proxy.HTTPProxy
	httpServer *http.Server
}

func New(cfg *appconfig.Config, c *cache.Cache, p *proxy.HTTPProxy) *Server {
	s := &Server{
		config: cfg,
		cache:  c,
		proxy:  p,
	}
	return s
}

func (s *Server) Run() error {
	// Setup access log middleware and handler
	accessLogPath := filepath.Join(s.config.LogfileDir, s.config.AccessLogFilename)

	// s.handler is defined in handler.go
	loggedHandler := logger.AccessLogMiddleware(accessLogPath)(http.HandlerFunc(s.handler))

	s.httpServer = &http.Server{
		Addr:    ":" + strconv.Itoa(s.config.ServerPort),
		Handler: loggedHandler,
	}

	// Start server in goroutine
	go func() {
		logger.Infof("Server started on %s", s.httpServer.Addr)
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("Server error: %v", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("Shutting down server...")

	return s.Shutdown()
}

func (s *Server) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := s.httpServer.Shutdown(ctx); err != nil {
		logger.Errorf("Server forced to shutdown: %v", err)
	}

	logger.Info("Closing cache...")
	if err := s.cache.Close(); err != nil {
		logger.Errorf("Error closing cache: %v", err)
		return err
	}
	logger.Info("Cache closed successfully")
	return nil
}
