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
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	ramOption, err := cache.WithRAMPolicy(cfg.RAMCacheAlgorithm, uint64(cfg.RAMCacheBytes))
	if err != nil {
		return nil, err
	}
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

	c, err := cache.NewCacheFromStore(store, numStripes, ramOption)
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
	fmt.Printf(`Razor - HTTP Object Cache Proxy

Usage:
  razor [options]

Options:
  -d          Run in daemon mode (background)
  -c string   Path to config directory (default: "conf")
  -p string   Path to PID file (optional, recommended for daemon mode)
  -h          Show this help message
  -v          Show version information

Examples:
  # Run in foreground
  razor

  # Run in background
  razor -d

  # Run with custom config and PID file
  razor -d -c /etc/razor -p /var/run/razor.pid

  # Show version
  razor -v

For more information, visit: https://github.com/Monsterwi/bakemono
`)
}

// printVersion displays version information
func printVersion() {
	fmt.Printf("Razor HTTP Object Cache Proxy\n")
	fmt.Printf("Version: %s\n", Version)
	fmt.Printf("Build Date: %s\n", BuildDate)
	fmt.Printf("Go Version: %s\n", runtime.Version())
}
