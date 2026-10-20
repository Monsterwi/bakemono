package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Monsterwi/razor/config"
)

func TestNetworkRecordsAndValidation(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "storage.config"), []byte("#unused\n"), 0600); err != nil {
		t.Fatal(err)
	}
	records := filepath.Join(dir, "records.config")
	write := func(data string) {
		t.Helper()
		if err := os.WriteFile(records, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("CONFIG proxy.config.ssl.cert_file STRING cert.pem\nCONFIG proxy.config.ssl.key_file STRING key.pem\nCONFIG proxy.config.http.upstream_ca_file STRING ca.pem\nCONFIG proxy.config.http.health.interval INT 0\nCONFIG proxy.config.http.health.path STRING /ready?local=1\n")
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TLSCertFile != "cert.pem" || cfg.TLSKeyFile != "key.pem" || cfg.UpstreamCAFile != "ca.pem" || cfg.HealthCheckInterval != 0 || cfg.HealthCheckPath != "/ready?local=1" || cfg.HealthCheckFailures != 2 || cfg.HealthCheckSuccesses != 2 {
		t.Fatalf("network settings not loaded: %+v", cfg)
	}
	for _, record := range []string{
		"proxy.config.ssl.cert_file STRING cert.pem",
		"proxy.config.ssl.key_file STRING key.pem",
		"proxy.config.http.health.interval INT -1",
		"proxy.config.http.health.interval INT bad",
		"proxy.config.http.health.timeout INT 0",
		"proxy.config.http.health.failures INT 0",
		"proxy.config.http.health.successes INT 1001",
		"proxy.config.http.health.path STRING https://other.example/",
	} {
		write("CONFIG " + record + "\n")
		if _, err := config.Load(dir); err == nil {
			t.Errorf("accepted invalid network record %q", record)
		}
	}
}
