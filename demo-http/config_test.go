package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDemoConfigPreservesExistingYAML(t *testing.T) {
	cfg, err := readConfig("config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Storage) != 2 || cfg.Storage[0].SizeMB != 4096 || cfg.Schema != "http" || cfg.Port != 80 || len(cfg.Location) != 1 || cfg.Location[0].Pattern != "/" || cfg.Upstream.BalanceMode != "consistent-hash" || len(cfg.Upstream.ProxyPass) != 2 || !cfg.TCPHealthCheck || cfg.HealthCheckInterval != 10 || cfg.RAMCacheSizeMB != 1024 || cfg.ZapConfig.Encoding != "console" || len(cfg.ZapConfig.OutputPaths) != 2 {
		t.Fatalf("existing config fields lost: %+v", cfg)
	}
}

func TestDemoConfigRejectsInvalidSettings(t *testing.T) {
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{
		{"schema: http", "schema: ftp"},
		{"port: 80", "port: 0"},
		{"size_mb: 4096", "size_mb: -1"},
		{"pattern: /", "pattern: relative"},
		{"health_check_interval: 10", "health_check_interval: 0"},
		{"schema: http", "schema: https"},
		{"health_check_interval: 10", "health_check_interval: 86401"},
		{"ssl_certificate:", "ssl_certificate: cert.pem"},
	} {
		t.Run(pair[1], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(strings.ReplaceAll(string(data), pair[0], pair[1])), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := readConfig(path); err == nil {
				t.Fatal("invalid demo config was accepted")
			}
		})
	}
}
