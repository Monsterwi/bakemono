package cache

import (
	"bytes"
	"fmt"
	"sync"
	"testing"
)

func TestRAMPoliciesSharedContract(t *testing.T) {
	for _, algorithm := range []string{"otter", "s3-fifo"} {
		t.Run(algorithm, func(t *testing.T) {
			c, err := NewRamCacheWithAlgorithm(64, algorithm)
			if err != nil {
				t.Fatal(err)
			}
			key := []byte("key")
			c.Put(key, []byte("old"))
			c.Put(key, []byte("new"))
			if v, ok := c.Get(key); !ok || string(v) != "new" {
				t.Fatalf("overwrite returned %q %v", v, ok)
			}
			c.CleanUp()
			if c.Items() != 1 || c.Size() != 3 {
				t.Fatalf("items=%d size=%d", c.Items(), c.Size())
			}
			c.Delete(key)
			c.CleanUp()
			if _, ok := c.Get(key); ok {
				t.Fatal("deleted value resurrected")
			}
			c.Put(key, []byte("again"))
			if v, ok := c.Get(key); !ok || string(v) != "again" {
				t.Fatal("reinsertion lost")
			}
			c.Put(key, make([]byte, 65))
			c.CleanUp()
			if _, ok := c.Get(key); ok {
				t.Fatal("oversize overwrite retained stale value")
			}
			c.Put([]byte("empty"), []byte{})
			if v, ok := c.Get([]byte("empty")); !ok || len(v) != 0 {
				t.Fatal("empty value lost")
			}
			for i := 0; i < 1000; i++ {
				c.Put([]byte(fmt.Sprintf("empty-%d", i)), []byte{})
			}
			c.CleanUp()
			if c.Size() > 64 || c.Items() > 64 {
				t.Fatalf("unbounded empty entries size=%d items=%d", c.Size(), c.Items())
			}
			st := c.Stats()
			if st.Hits == 0 || st.Misses == 0 || st.Evictions == 0 || st.CapacityBytes != 64 || st.Algorithm != algorithm {
				t.Fatalf("stats=%+v", st)
			}
		})
	}
	if _, err := NewRamCacheWithAlgorithm(64, "unknown"); err == nil {
		t.Fatal("unknown algorithm accepted")
	}
}

func TestRAMPoliciesConcurrentVisibility(t *testing.T) {
	for _, algorithm := range []string{"otter", "s3-fifo"} {
		t.Run(algorithm, func(t *testing.T) {
			c, err := NewRamCacheWithAlgorithm(4096, algorithm)
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Go(func() {
					for j := 0; j < 500; j++ {
						key := []byte(fmt.Sprintf("k-%d", j%16))
						c.Put(key, []byte("new"))
						v, ok := c.Get(key)
						if ok && !bytes.Equal(v, []byte("new")) {
							t.Error("stale value")
						}
						c.Delete(key)
						c.Stats()
					}
				})
			}
			wg.Wait()
			c.CleanUp()
			for j := 0; j < 16; j++ {
				key := []byte(fmt.Sprintf("k-%d", j))
				c.Delete(key)
			}
			c.CleanUp()
			if c.Items() != 0 {
				t.Fatal("completed deletion resurrected")
			}
		})
	}
}

func TestS3FIFOPromotionGhostAndDeletion(t *testing.T) {
	c, err := NewRamCacheWithAlgorithm(4096, "s3-fifo")
	if err != nil {
		t.Fatal(err)
	}
	value := make([]byte, 512)
	for i := 0; i < 8; i++ {
		c.Put([]byte(fmt.Sprintf("k%d", i)), value)
	}
	c.Get([]byte("k0"))
	c.Get([]byte("k0"))
	c.Put([]byte("k8"), value)
	st := c.Stats()
	if st.Promotions != 1 || st.MainEntries != 1 || st.GhostEntries != 1 {
		t.Fatalf("small reuse not promoted / cold key not ghosted: %+v", st)
	}
	if _, ok := c.Get([]byte("k1")); ok {
		t.Fatal("ghost returned body")
	}
	c.Put([]byte("k1"), value)
	st = c.Stats()
	if st.GhostHits != 1 || st.MainEntries != 2 {
		t.Fatalf("ghost not admitted to main: %+v", st)
	}
	for i := 9; i < 100; i++ {
		c.Put([]byte(fmt.Sprintf("k%d", i)), value)
	}
	st = c.Stats()
	if st.ChargedBytes > 4096 || st.GhostBytes > 4096 || st.GhostEntries > 8 {
		t.Fatalf("unbounded queues: %+v", st)
	}
	// A deleted ghost must not influence a subsequent admission or revive a value.
	c.Delete([]byte("k93"))
	before := c.Stats().GhostHits
	c.Put([]byte("k93"), value)
	if c.Stats().GhostHits != before {
		t.Fatal("Delete retained ghost history")
	}
	c.Put(bytes.Repeat([]byte("k"), maxRAMKeyBytes+1), value)
	if _, ok := c.Get(bytes.Repeat([]byte("k"), maxRAMKeyBytes+1)); ok {
		t.Fatal("oversize key admitted")
	}
}

func TestS3FIFOWritesCountAsReferencesWithoutReplacingQueuePosition(t *testing.T) {
	c, err := NewRamCacheWithAlgorithm(4096, "s3-fifo")
	if err != nil {
		t.Fatal(err)
	}
	value := make([]byte, 512)
	for i := 0; i < 8; i++ {
		c.Put([]byte(fmt.Sprintf("k%d", i)), value)
	}
	c.Put([]byte("k0"), value)
	c.Put([]byte("k0"), value)
	c.Put([]byte("k8"), value)
	if st := c.Stats(); st.Promotions != 1 || st.MainEntries != 1 {
		t.Fatalf("resident updates lost reference frequency / FIFO position: %+v", st)
	}
}
