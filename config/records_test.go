package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRecordsConfig(t *testing.T) {
	// Create a temporary records.config file for testing
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "records.config")

	configContent := `# Test configuration
CONFIG proxy.config.http.server_ports STRING 8080
CONFIG proxy.config.log.logfile_dir STRING /tmp/test/logs
CONFIG proxy.config.http.keep_alive_no_activity_timeout_in INT 120
CONFIG proxy.config.http.parent_proxy.upstream_targets STRING http://server1.com,http://server2.com
`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to create test config file: %v", err)
	}

	// Load the configuration
	rc, err := LoadRecordsConfig(configPath)
	if err != nil {
		t.Fatalf("Failed to load records config: %v", err)
	}

	// Test GetString
	serverPorts := rc.GetString("proxy.config.http.server_ports", "80")
	if serverPorts != "8080" {
		t.Errorf("Expected server_ports to be '8080', got '%s'", serverPorts)
	}

	// Test GetString with default
	notExist := rc.GetString("proxy.config.not.exist", "default")
	if notExist != "default" {
		t.Errorf("Expected default value 'default', got '%s'", notExist)
	}

	// Test GetInt
	timeout := rc.GetInt("proxy.config.http.keep_alive_no_activity_timeout_in", 60)
	if timeout != 120 {
		t.Errorf("Expected timeout to be 120, got %d", timeout)
	}

	// Test GetStringSlice
	upstreams := rc.GetStringSlice("proxy.config.http.parent_proxy.upstream_targets", nil)
	if len(upstreams) != 2 {
		t.Errorf("Expected 2 upstream targets, got %d", len(upstreams))
	}
	if upstreams[0] != "http://server1.com" {
		t.Errorf("Expected first upstream to be 'http://server1.com', got '%s'", upstreams[0])
	}

	// Test Has
	if !rc.Has("proxy.config.http.server_ports") {
		t.Error("Expected config key to exist")
	}
	if rc.Has("proxy.config.not.exist") {
		t.Error("Expected config key to not exist")
	}
}
