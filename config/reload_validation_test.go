package config_test

import (
	"github.com/Monsterwi/razor/config"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRejectsMalformedRecognizedSettings(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "storage.config"), []byte("# unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, record := range []string{
		"CONFIG proxy.config.http.server_ports INT not-a-number",
		"CONFIG proxy.config.http.server_ports INT 65536",
		"CONFIG proxy.config.http.parent_proxy.upstream_targets STRING ,",
		"CONFIG proxy.config.http.parent_proxy.upstream_targets STRING http://good.test,,http://other.test",
		"CONFIG proxy.config.http.parent_proxy.upstream_targets STRING file:///tmp/file",
		"CONFIG proxy.config.http.parent_proxy.upstream_algo STRING unknown",
		"CONFIG proxy.config.http.trusted_proxies STRING invalid",
		"CONFIG proxy.config.http.max_requests INT",
		"CONFIG proxy.config.http.max_requests BOOLEAN 10",
		"CONFIG proxy.config.http.max_requests INT 10\nCONFIG proxy.config.http.max_requests INT 20",
	} {
		t.Run(record, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, "records.config"), []byte(record+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Load(dir); err == nil {
				t.Fatal("invalid candidate silently accepted")
			}
		})
	}
}

func TestValidateUpstreamTCPPort(t *testing.T) {
	for _, target := range []string{"http://origin.test", "https://origin.test", "http://origin.test:80", "https://origin.test:443", "http://origin.test:65535", "https://[::1]:65535"} {
		t.Run(target, func(t *testing.T) {
			cfg := config.GetDefaultConfig()
			cfg.UpstreamTargets = []string{target}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("valid upstream rejected: %v", err)
			}
		})
	}
	for _, port := range []string{"0", "65536", "18446744073709551616"} {
		t.Run(port, func(t *testing.T) {
			cfg := config.GetDefaultConfig()
			cfg.UpstreamTargets = []string{"http://127.0.0.1:" + port}
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid destination TCP port accepted")
			}
		})
	}
}
