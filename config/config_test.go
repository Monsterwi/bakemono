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
CONFIG proxy.config.log.logfile_dir STRING /tmp/razor/logs
CONFIG proxy.config.http.trusted_proxies STRING 192.0.2.0/24,2001:db8::/32
CONFIG proxy.config.http.purge_token STRING test-only-purge-secret
`

	storageConfigPath := filepath.Join(tmpDir, "storage.config")
	storageConfigContent := `# Test configuration
/tmp/razor/cache1.db 100M
/tmp/razor/cache2.db 100M
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

	if len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[0] != "192.0.2.0/24" || cfg.TrustedProxies[1] != "2001:db8::/32" {
		t.Fatalf("trusted proxies = %v", cfg.TrustedProxies)
	}

	if cfg.PurgeToken != "test-only-purge-secret" {
		t.Fatal("PURGE token not loaded")
	}

	// Verify loaded values
	if cfg.ServerPort != 9090 {
		t.Errorf("Expected ServerPort to be 9090, got %d", cfg.ServerPort)
	}

	if cfg.LogfileDir != "/tmp/razor/logs" {
		t.Errorf("Expected LogfileDir to be '/tmp/razor/logs', got '%s'", cfg.LogfileDir)
	}

}

func TestGetDefaultConfig(t *testing.T) {
	cfg := GetDefaultConfig()

	if cfg == nil {
		t.Fatal("Expected non-nil config")
	}

	if cfg.PurgeToken != "" {
		t.Fatal("default config enables PURGE")
	}

	if cfg.ServerPort != 80 {
		t.Errorf("Expected default ServerPort to be 80, got %d", cfg.ServerPort)
	}

	if cfg.LogfileDir != "/var/log/razor" {
		t.Errorf("Expected default LogfileDir to be '/var/log/razor', got '%s'", cfg.LogfileDir)
	}

	if len(cfg.UpstreamTargets) != 2 {
		t.Errorf("Expected 2 default upstream targets, got %d", len(cfg.UpstreamTargets))
	}
}

func TestResourceDefaultsAndInvalidRecords(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "storage.config"), []byte("# unused"), 0600); err != nil {
		t.Fatal(err)
	}
	records := filepath.Join(dir, "records.config")
	if err := os.WriteFile(records, []byte("# defaults"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxRequests < 1 || cfg.MaxOriginRequests < 1 || cfg.CacheWriteBufferBytes < 1 || cfg.RequestHeaderTimeout < 1 || cfg.SharedFillMaxTasks < 1 || cfg.SharedFillMaxWaiters < 100 || cfg.SharedFillTimeout < 1 {
		t.Fatalf("missing safety defaults: %+v", cfg)
	}
	for _, value := range []string{"0", "-1", "bad", "999999999999999999999999"} {
		if err := os.WriteFile(records, []byte("CONFIG proxy.config.http.max_requests INT "+value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err == nil {
			t.Fatalf("accepted limit %q", value)
		}
	}
}

func TestCoalescingLimitsLoadAndRejectInvalidValues(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "storage.config"), []byte("# unused"), 0600); err != nil {
		t.Fatal(err)
	}
	records := filepath.Join(dir, "records.config")
	if err := os.WriteFile(records, []byte("CONFIG proxy.config.cache.shared_fill_max_tasks INT 3\nCONFIG proxy.config.cache.shared_fill_max_waiters INT 100\nCONFIG proxy.config.cache.shared_fill_timeout INT 600\nCONFIG proxy.config.cache.max_object_bytes INT 209715200\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SharedFillMaxTasks != 3 || cfg.SharedFillMaxWaiters != 100 || cfg.SharedFillTimeout != 600 || cfg.MaxCacheObjectBytes != 200<<20 {
		t.Fatalf("limits=%+v", cfg)
	}
	for _, field := range []string{"shared_fill_max_tasks", "shared_fill_max_waiters", "shared_fill_timeout"} {
		for _, value := range []string{"0", "-1", "invalid", "1000001"} {
			if err := os.WriteFile(records, []byte("CONFIG proxy.config.cache."+field+" INT "+value+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(dir); err == nil {
				t.Fatalf("accepted %s=%s", field, value)
			}
		}
	}
}
