package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicStaticRecordsRejectInvalidMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "storage.config"), []byte("# unused"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"-1", "2", "true"} {
		if err := os.WriteFile(filepath.Join(dir, "records.config"), []byte("CONFIG proxy.config.cache.shared_fill_public_static INT "+value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(dir); err == nil {
			t.Fatalf("accepted public static mode %s", value)
		}
	}
}

func TestPublicStaticRecordsLoadAndDefault(t *testing.T) {
	if GetDefaultConfig().SharedFillPublicStatic != 0 {
		t.Fatal("public normalization enabled by default")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "storage.config"), []byte("# unused"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []int{0, 1} {
		if err := os.WriteFile(filepath.Join(dir, "records.config"), []byte(fmt.Sprintf("CONFIG proxy.config.cache.shared_fill_public_static INT %d\n", mode)), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.SharedFillPublicStatic != mode {
			t.Fatalf("loaded mode %d want %d", cfg.SharedFillPublicStatic, mode)
		}
	}
}
