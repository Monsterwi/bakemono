package cache

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestRAMPoliciesDiskFallbackInvalidationAndSwitch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stripe")
	var c *Cache
	open := func(algorithm string) {
		t.Helper()
		opt, err := WithRAMPolicy(algorithm, 32<<10)
		if err != nil {
			t.Fatal(err)
		}
		c, err = NewCacheWithOffsets(1, []string{path}, []int64{4 << 20}, nil, opt)
		if err != nil {
			t.Fatal(err)
		}
		if err = c.Init(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
	}
	payload := bytes.Repeat([]byte("large"), 16<<10)
	open("otter")
	writeStorageObject(t, c, "large", payload)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	for _, algorithm := range []string{"s3-fifo", "otter"} {
		open(algorithm)
		got, err := readStorageObject(c, "large")
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("%s disk recovery: %d %v", algorithm, len(got), err)
		}
		writeStorageObject(t, c, "updated", []byte("old"))
		writeStorageObject(t, c, "updated", []byte("new"))
		got, err = readStorageObject(c, "updated")
		if err != nil || string(got) != "new" {
			t.Fatalf("%s overwrite: %q %v", algorithm, got, err)
		}
		if err = c.Remove([]byte("updated")); err != nil {
			t.Fatal(err)
		}
		if got, err = readStorageObject(c, "updated"); err == nil {
			t.Fatalf("%s removed object resurrected: %q", algorithm, got)
		}
		if _, ok := c.Stripes[0].RamCache.Get([]byte("updated")); ok {
			t.Fatal("removed object retained in RAM")
		}
		writeStorageObject(t, c, "updated", []byte("replacement"))
		got, err = readStorageObject(c, "updated")
		if err != nil || string(got) != "replacement" {
			t.Fatalf("reinsert: %q %v", got, err)
		}
		if err = c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
