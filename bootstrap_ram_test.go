package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Monsterwi/razor/config"
)

func TestBootstrapRAMPolicySelectionAndDisable(t *testing.T) {
	for _, algorithm := range []string{"otter", "s3-fifo"} {
		for _, capacity := range []int{0, 8192} {
			t.Run(algorithm+"/"+fmt.Sprint(capacity), func(t *testing.T) {
				dir := t.TempDir()
				cfg := config.GetDefaultConfig()
				cfg.ConfigDir = dir
				cfg.RAMCacheAlgorithm = algorithm
				cfg.RAMCacheBytes = capacity
				if err := os.WriteFile(filepath.Join(dir, "storage.config"), []byte(filepath.Join(dir, "stripe")+" 4M\n"), 0600); err != nil {
					t.Fatal(err)
				}
				c, err := NewCache(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				ram := c.Stripes[0].RamCache
				if capacity == 0 {
					if ram != nil {
						t.Fatal("zero budget did not disable RAM")
					}
					return
				}
				if ram == nil {
					t.Fatal("configured RAM disabled")
				}
				if st := ram.Stats(); st.Algorithm != algorithm || st.CapacityBytes != 8192 {
					t.Fatalf("bootstrap policy=%+v", st)
				}
			})
		}
	}
}
