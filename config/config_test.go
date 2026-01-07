package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad(t *testing.T) {
	// Create a temporary records.config file for testing
	tmpDir := t.TempDir()
	recordsConfigPath := filepath.Join(tmpDir, "records.config")

	configContent := `# Test configuration
CONFIG proxy.config.http.server_addr STRING :9090
CONFIG proxy.config.http.server_ports STRING 9090
CONFIG proxy.config.log.logfile_dir STRING /tmp/bakemono/logs
`

	storageConfigPath := filepath.Join(tmpDir, "storage.config")
	storageConfigContent := `# Test configuration
/tmp/bakemono/cache1.db 100M
/tmp/bakemono/cache2.db 100M
`
	if err := os.WriteFile(storageConfigPath, []byte(storageConfigContent), 0644); err != nil {
		t.Fatalf("Failed to create test storage config file: %v", err)
	}

	if err := os.WriteFile(recordsConfigPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to create test config file: %v", err)
	}

	// Load the configuration
	cfg, err := Load(tmpDir)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Verify loaded values
	if cfg.ServerPort != 9090 {
		t.Errorf("Expected ServerPort to be 9090, got %d", cfg.ServerPort)
	}

	if cfg.LogfileDir != "/tmp/bakemono/logs" {
		t.Errorf("Expected LogfileDir to be '/tmp/bakemono/logs', got '%s'", cfg.LogfileDir)
	}

}

func TestGetDefaultConfig(t *testing.T) {
	cfg := GetDefaultConfig()

	if cfg == nil {
		t.Fatal("Expected non-nil config")
	}

	if cfg.ServerPort != 80 {
		t.Errorf("Expected default ServerPort to be 80, got %d", cfg.ServerPort)
	}

	if cfg.LogfileDir != "/var/log/bakemono" {
		t.Errorf("Expected default LogfileDir to be '/var/log/bakemono', got '%s'", cfg.LogfileDir)
	}

	if len(cfg.UpstreamTargets) != 2 {
		t.Errorf("Expected 2 default upstream targets, got %d", len(cfg.UpstreamTargets))
	}
}
