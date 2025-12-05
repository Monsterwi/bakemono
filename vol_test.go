package bakemono

import (
	"fmt"
	"io"
	"os"
	"sync"
	"testing"
)

func CreateTestingVol(path string, fileSize, chunkSize uint64) (*Vol, bool, error) {
	cfg, err := NewDefaultVolOptions(path, fileSize, chunkSize, 10000)
	if err != nil {
		panic(err)
	}
	v := &Vol{}
	corrupted, err := v.Init(cfg)
	if err != nil {
		panic(err)
	}
	return v, corrupted, err
}

func TestInitVol(t *testing.T) {
	_, _, err := CreateTestingVol("/tmp/bakemono-test.vol", 1024*1024*200, 1024*1024)
	defer func() {
		err := os.Remove("/tmp/bakemono-test.vol")
		if err != nil {
			t.Error(err)
		}
	}()
	if err != nil {
		t.Error(err)
	}
}

func TestVolWriteReadFileWithClose(t *testing.T) {
	v, _, err := CreateTestingVol("/tmp/bakemono-test.vol", 1024*1024*200, 1024*1024)
	defer func() {
		err := os.Remove("/tmp/bakemono-test.vol")
		if err != nil {
			t.Error(err)
		}
	}()
	if err != nil {
		t.Fatal(err)
	}
	err = v.Set([]byte("key"), []byte("value"))
	if err != nil {
		t.Fatal(err)
	}
	hit, reader, err := v.Get([]byte("key"))
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("key should be hit")
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "value" {
		t.Fatal("value should be 'value'")
	}

	// Manual flush removed in Stripe architecture
	// err = v.flushMetaToFp()

	// Re-open
	v.Close()

	// Test Persistence
	v2, _, err := CreateTestingVol("/tmp/bakemono-test.vol", 1024*1024*200, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer v2.Close()

	hit, reader, err = v2.Get([]byte("key"))
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("key should be hit after reopen")
	}
	data, err = io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "value" {
		t.Fatalf("value should be 'value', got '%s'", string(data))
	}
}

func TestVolPersistence(t *testing.T) {
	path := "/tmp/bakemono-test.vol"
	fileSize := uint64(1024 * 1024 * 128 * 2) // 256MB (2 stripes)
	chunkSize := uint64(1024 * 1024)

	os.Remove(path)
	defer os.Remove(path)

	// 1. Create and Write
	{
		v, _, err := CreateTestingVol(path, fileSize, chunkSize)
		if err != nil {
			t.Fatal(err)
		}
		// Small objects
		if err := v.Set([]byte("k1"), []byte("v1")); err != nil {
			t.Fatal(err)
		}
		if err := v.Set([]byte("k2"), []byte("v2")); err != nil {
			t.Fatal(err)
		}

		// Multi-fragment object (ChunkDataSize is 4MB)
		// We use 10MB object
		largeVal := make([]byte, 10*1024*1024)
		// Fill with pattern
		for i := range largeVal {
			largeVal[i] = byte(i % 256)
		}
		if err := v.Set([]byte("k_large"), largeVal); err != nil {
			t.Fatal(err)
		}

		// Concurrent writes
		var wg sync.WaitGroup
		errChan := make(chan error, 10)
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				key := []byte(fmt.Sprintf("k_conc_%d", i))
				val := []byte(fmt.Sprintf("v_conc_%d", i))
				if err := v.Set(key, val); err != nil {
					errChan <- err
				}
			}(i)
		}
		wg.Wait()
		close(errChan)
		for err := range errChan {
			if err != nil {
				t.Fatal(err)
			}
		}

		v.Close()
	}

	// 2. Re-open
	{
		v, _, err := CreateTestingVol(path, fileSize, chunkSize)
		if err != nil {
			t.Fatal(err)
		}
		defer v.Close()

		// Read k1
		hit, reader, err := v.Get([]byte("k1"))
		if err != nil || !hit {
			t.Fatal("k1 missing")
		}
		val, _ := io.ReadAll(reader)
		if string(val) != "v1" {
			t.Fatal("k1 mismatch")
		}

		// Read k2
		hit, reader, err = v.Get([]byte("k2"))
		if err != nil || !hit {
			t.Fatal("k2 missing")
		}
		val, _ = io.ReadAll(reader)
		if string(val) != "v2" {
			t.Fatal("k2 mismatch")
		}

		// Read k_large
		hit, reader, err = v.Get([]byte("k_large"))
		if err != nil || !hit {
			t.Fatal("k_large missing")
		}
		largeValRead, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read k_large failed: %v", err)
		}
		if len(largeValRead) != 10*1024*1024 {
			t.Fatalf("k_large length mismatch: expected %d, got %d", 10*1024*1024, len(largeValRead))
		}
		for i := range largeValRead {
			if largeValRead[i] != byte(i%256) {
				t.Fatalf("k_large content mismatch at index %d", i)
			}
		}

		// Read concurrent keys
		for i := 0; i < 10; i++ {
			key := []byte(fmt.Sprintf("k_conc_%d", i))
			expectedVal := fmt.Sprintf("v_conc_%d", i)
			hit, reader, err := v.Get(key)
			if err != nil || !hit {
				t.Fatalf("k_conc_%d missing", i)
			}
			val, _ := io.ReadAll(reader)
			if string(val) != expectedVal {
				t.Fatalf("k_conc_%d mismatch", i)
			}
		}
	}
}
