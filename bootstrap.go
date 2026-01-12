package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/Monsterwi/razor/cache"
	appconfig "github.com/Monsterwi/razor/config"
	"github.com/Monsterwi/razor/logger"
)

// initLogger initializes the logger with the configured log directory
func initLogger(cfg *appconfig.Config, isDaemon bool) error {
	// Initialize production logger with configured paths
	// Pass daemon mode to avoid writing to closed stdout/stderr
	_, err := logger.InitLogger(cfg.LogfileDir, cfg.ErrorLogFilename, isDaemon)
	if err != nil {
		return fmt.Errorf("failed to initialize logger: %w", err)
	}

	logger.Infof("Logger initialized successfully (daemon mode: %v)", isDaemon)
	logger.Infof("Log directory: %s", cfg.LogfileDir)
	logger.Infof("Access log: %s/%s", cfg.LogfileDir, cfg.AccessLogFilename)
	logger.Infof("Error log: %s/%s", cfg.LogfileDir, cfg.ErrorLogFilename)

	return nil
}

// NewCache builds and initializes cache stripes from the provided config.
func NewCache(cfg *appconfig.Config) (*cache.Cache, error) {
	storageConfigPath := filepath.Join(cfg.ConfigDir, appconfig.StorageConfigFilename)
	store, err := appconfig.LoadStore(storageConfigPath)
	if err != nil {
		return nil, fmt.Errorf("load store: %w", err)
	}

	numStripes := len(store.Spans)
	if numStripes == 0 {
		return nil, errors.New("no spans available for cache")
	}

	logger.Infof("Configuration loaded: %d spans, will create %d stripes", len(store.Spans), numStripes)
	logger.Infof("Initializing cache with %d stripes from %d spans", numStripes, len(store.Spans))

	c, err := cache.NewCacheFromStore(store, numStripes)
	if err != nil {
		return nil, fmt.Errorf("create cache: %w", err)
	}

	if err := c.Init(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("init cache: %w", err)
	}

	return c, nil
}

// printHelp displays usage information
func printHelp() {
	fmt.Printf(`Bakemono - High Performance CDN Cache Server

Usage:
  bakemono [options]

Options:
  -d          Run in daemon mode (background)
  -c string   Path to records.config file (default: "conf/records.config")
  -p string   Path to PID file (optional, recommended for daemon mode)
  -h          Show this help message
  -v          Show version information

Examples:
  # Run in foreground
  bakemono

  # Run in background
  bakemono -d

  # Run with custom config and PID file
  bakemono -d -c /etc/bakemono/records.config -p /var/run/bakemono.pid

  # Show version
  bakemono -v

For more information, visit: https://github.com/Monsterwi/bakemono
`)
}

// printVersion displays version information
func printVersion() {
	fmt.Printf("Bakemono CDN Cache Server\n")
	fmt.Printf("Version: %s\n", Version)
	fmt.Printf("Build Date: %s\n", BuildDate)
	fmt.Printf("Go Version: %s\n", runtime.Version())
}
