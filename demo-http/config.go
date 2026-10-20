package main

import (
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
	"go.uber.org/zap"
)

// demoConfig keeps the standalone demo's YAML format; production uses records.config.
type demoConfig struct {
	Storage []struct {
		Path   string `yaml:"path"`
		SizeMB int64  `yaml:"size_mb"`
	} `yaml:"storage"`
	RAMCacheSizeMB    uint64 `yaml:"ram_cache_size_mb"`
	Schema            string `yaml:"schema"`
	Port              int    `yaml:"port"`
	UpstreamCAFile    string `yaml:"upstream_ca_file"`
	SSLCertificate    string `yaml:"ssl_certificate"`
	SSLCertificateKey string `yaml:"ssl_certificate_key"`
	Location          []struct {
		Pattern string `yaml:"pattern"`
	} `yaml:"location"`
	TCPHealthCheck      bool `yaml:"tcp_health_check"`
	HealthCheckInterval uint `yaml:"health_check_interval"`
	Upstream            struct {
		ProxyPass   []string `yaml:"proxy_pass"`
		BalanceMode string   `yaml:"balance_mode"`
	} `yaml:"upstream"`
	AccessLogPath string     `yaml:"access_log_path"`
	ZapConfig     zap.Config `yaml:"zap_config"`
}

func readConfig(path string) (*demoConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg demoConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Schema != "http" && cfg.Schema != "https" {
		return nil, fmt.Errorf("unsupported schema %q", cfg.Schema)
	}
	if cfg.Port < 1 || cfg.Port > 65535 {
		return nil, fmt.Errorf("invalid port %d", cfg.Port)
	}
	if len(cfg.Storage) == 0 || len(cfg.Upstream.ProxyPass) == 0 {
		return nil, fmt.Errorf("storage and upstream are required")
	}
	for _, span := range cfg.Storage {
		if span.Path == "" || span.SizeMB <= 0 || span.SizeMB > (1<<63-1)/(1024*1024) {
			return nil, fmt.Errorf("invalid storage span")
		}
	}
	if cfg.RAMCacheSizeMB > (1<<64-1)/(1024*1024) {
		return nil, fmt.Errorf("invalid RAM cache size")
	}
	if cfg.HealthCheckInterval > 86400 || (cfg.TCPHealthCheck && cfg.HealthCheckInterval == 0) {
		return nil, fmt.Errorf("enabled health check interval must be between 1 and 86400 seconds")
	}
	if cfg.Schema == "https" && (cfg.SSLCertificate == "" || cfg.SSLCertificateKey == "") {
		return nil, fmt.Errorf("HTTPS requires a certificate and key")
	}
	if cfg.Schema == "http" && (cfg.SSLCertificate != "" || cfg.SSLCertificateKey != "") {
		return nil, fmt.Errorf("TLS certificate settings require https schema")
	}
	for _, location := range cfg.Location {
		if location.Pattern == "" || location.Pattern[0] != '/' {
			return nil, fmt.Errorf("location must start with /")
		}
	}
	return &cfg, nil
}
