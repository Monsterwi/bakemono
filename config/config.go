package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Monsterwi/razor/balancer"
	"github.com/Monsterwi/razor/cache"
	"github.com/Monsterwi/razor/logger"
)

const (
	RecordsConfigFilename = "records.config"
	StorageConfigFilename = "storage.config"
	ParentConfigFilename  = "parent.config"
)

// Config holds the application configuration
type Config struct {
	MaxClientConnections int
	resourceDefaults     bool
	CacheWriteTimeout    int
	// Config directory
	ConfigDir string

	// Server configuration
	ServerPort int

	// Upstream configuration
	UpstreamTargets []string
	UpstreamAlgo    string
	TrustedProxies  []string

	// Logging configuration
	LogfileDir        string
	AccessLogFilename string
	ErrorLogFilename  string

	MaxRequests, MaxQueuedRequests                                                                                  int
	MaxOriginRequests, MaxOriginQueuedRequests, MaxOriginConnections                                                int
	MaxCacheWriters, MaxQueuedCacheWriters, CacheWriteBufferBytes, MaxCacheObjectBytes                              int
	RequestHeaderTimeout, ConnectTimeout, TLSHandshakeTimeout, ResponseHeaderTimeout, QueueTimeout, ShutdownTimeout int

	// Timeout configuration
	KeepAliveTimeout      int
	TransactionTimeoutIn  int
	TransactionTimeoutOut int
}

// GetDefaultConfig returns default configuration values
func GetDefaultConfig() *Config {
	return (&Config{
		ConfigDir:             "conf",
		ServerPort:            80,
		UpstreamAlgo:          balancer.ConsistentHashBalancer,
		UpstreamTargets:       []string{"http://10.62.216.7", "http://10.62.216.8"},
		LogfileDir:            "/var/log/bakemono",
		AccessLogFilename:     "access.log",
		ErrorLogFilename:      "error.log",
		KeepAliveTimeout:      120,
		TransactionTimeoutIn:  30,
		TransactionTimeoutOut: 30,
	}).WithDefaults()
}

// Load loads configuration from records.config file
func Load(configDir string) (*Config, error) {
	if configDir == "" {
		cfg := GetDefaultConfig()
		return cfg, nil
	}

	recordsConfigPath := filepath.Join(configDir, RecordsConfigFilename)
	storageConfigPath := filepath.Join(configDir, StorageConfigFilename)
	// Check if records.config exists
	if _, err := os.Stat(recordsConfigPath); err != nil {
		return nil, fmt.Errorf("records.config not found at %s", recordsConfigPath)
	}
	if _, err := os.Stat(storageConfigPath); err != nil {
		return nil, fmt.Errorf("storage.config not found at %s", storageConfigPath)
	}

	cfg := GetDefaultConfig()
	cfg.ConfigDir = configDir
	// Load records.config
	rc, err := LoadRecordsConfig(recordsConfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load records.config: %w", err)
	}

	// Apply configuration from records.config
	// cfg.ServerAddr = rc.GetString("proxy.config.http.server_addr", cfg.ServerAddr)
	cfg.ServerPort = rc.GetInt("proxy.config.http.server_ports", cfg.ServerPort)
	cfg.LogfileDir = rc.GetString("proxy.config.log.logfile_dir", cfg.LogfileDir)
	cfg.AccessLogFilename = rc.GetString("proxy.config.log.access_log_filename", cfg.AccessLogFilename)
	cfg.ErrorLogFilename = rc.GetString("proxy.config.log.error_log_filename", cfg.ErrorLogFilename)
	cfg.UpstreamAlgo = rc.GetString("proxy.config.http.parent_proxy.upstream_algo", cfg.UpstreamAlgo)
	cfg.UpstreamTargets = rc.GetStringSlice("proxy.config.http.parent_proxy.upstream_targets", cfg.UpstreamTargets)
	cfg.TrustedProxies = rc.GetStringSlice("proxy.config.http.trusted_proxies", cfg.TrustedProxies)
	if err := cfg.loadLimits(rc); err != nil {
		return nil, err
	}

	logger.Infof("Configuration loaded from %s", recordsConfigPath)
	return cfg, nil
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
