package main

import (
	"fmt"
	"os"

	"github.com/Monsterwi/razor/cache"
	"github.com/Monsterwi/razor/logger"
)

// Config holds the application configuration
type Config struct {
	// Storage configuration
	StorageConfigPath string

	// Server configuration
	ServerAddr string
	ServerPort int

	// Upstream configuration
	UpstreamTargets []string
	UpstreamAlgo    string
}

func GetDefaultConfig() *Config {
	return &Config{
		StorageConfigPath: "conf/storage.config",
		ServerAddr:        ":80",
		ServerPort:        80,
		UpstreamAlgo:      "round-robin",
		UpstreamTargets:   []string{"http://10.62.216.7"},
	}
}

// LoadStore loads storage configuration and returns a Store instance
func LoadStore(configPath string) (*cache.Store, error) {
	store := cache.NewStore()

	if _, err := os.Stat(configPath); err != nil {
		return nil, fmt.Errorf("storage config file not found: %w", err)
	}

	if err := store.ReadConfig(configPath); err != nil {
		return nil, fmt.Errorf("failed to read storage config: %w", err)
	}

	logger.Infof("Loaded %d spans from storage.config", len(store.Spans))
	return store, nil
}
