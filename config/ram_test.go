package config_test

import (
	"fmt"
	"github.com/Monsterwi/razor/config"
	"os"
	"path/filepath"
	"testing"
)

func TestRAMConfigRejectsInvalidStrategyAndBudget(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "storage.config"), []byte("# preserved\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, record := range []string{
		"CONFIG proxy.config.cache.ram_cache_algorithm STRING unknown",
		"CONFIG proxy.config.cache.ram_cache_algorithm INT otter",
		"CONFIG proxy.config.cache.ram_cache_bytes INT -1",
		"CONFIG proxy.config.cache.ram_cache_bytes INT 1099511627777",
		"CONFIG proxy.config.cache.ram_cache_bytes INT invalid",
		"CONFIG proxy.config.cache.ram_cache_bytes STRING 10",
	} {
		t.Run(record, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(dir, "records.config"), []byte(record+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := config.Load(dir); err == nil {
				t.Fatal("invalid RAM configuration accepted")
			}
		})
	}
}

func TestRAMConfigDefaultsSelectionAndDisable(t *testing.T) {
	defaults := config.GetDefaultConfig()
	if defaults.RAMCacheAlgorithm != "otter" || defaults.RAMCacheBytes != 10<<20 {
		t.Fatalf("changed default: %s %d", defaults.RAMCacheAlgorithm, defaults.RAMCacheBytes)
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "storage.config"), []byte("# preserved\n"), 0600)
	for _, record := range []struct {
		algorithm string
		capacity  int
	}{{"otter", 0}, {"s3-fifo", 10485760}, {"otter", 1099511627776}} {
		text := fmt.Sprintf("CONFIG proxy.config.cache.ram_cache_algorithm STRING %s\nCONFIG proxy.config.cache.ram_cache_bytes INT %d\n", record.algorithm, record.capacity)
		if err := os.WriteFile(filepath.Join(dir, "records.config"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		c, err := config.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		c = c.WithDefaults()
		if c.RAMCacheAlgorithm != record.algorithm || c.RAMCacheBytes != record.capacity {
			t.Fatalf("parsed RAM config changed %s %d", c.RAMCacheAlgorithm, c.RAMCacheBytes)
		}
	}
}
