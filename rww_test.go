package bakemono

import (
	"bytes"
	"crypto/rand"
	"io"
	"os"
	"testing"
	"time"
)

func TestRWW(t *testing.T) {
	path := "/tmp/bakemono-stripe-rww.vol"
	stripeSize := int64(1024 * 1024 * 128) // 128MB
	avgChunkSize := uint64(1024 * 1024)

	os.Remove(path)
	defer os.Remove(path)

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(stripeSize); err != nil {
		t.Fatal(err)
	}

	s := NewStripe(0, f, 0, stripeSize, 0)
	corrupted, err := s.Init(avgChunkSize)
	if err != nil && !corrupted {
		t.Fatal(err)
	}

	// Generate Data larger than ChunkDataSize to ensure fragmentation
	testDataSize := int(ChunkDataSize)*2 + 1024
	data := make([]byte, testDataSize)
	rand.Read(data)
	key := []byte("rww-test-key")

	ready := make(chan struct{})

	// 1. Start Writer in Goroutine
	go func() {
		w, err := s.NewWriter(key)
		if err != nil {
			t.Errorf("NewWriter failed: %v", err)
			return
		}
		close(ready) // Signal ready

		// Write in chunks to simulate stream (but not too slow)
		writeChunk := 256 * 1024 // 256KB chunks for faster test
		for i := 0; i < len(data); i += writeChunk {
			end := i + writeChunk
			if end > len(data) {
				end = len(data)
			}
			w.Write(data[i:end])
			// Occasional yield to allow reader to consume
			if i%(writeChunk*4) == 0 {
				time.Sleep(1 * time.Millisecond)
			}
		}
		w.Close()
	}()

	// Wait for writer to start
	<-ready
	// Give it a tiny head start
	time.Sleep(5 * time.Millisecond)

	// 2. Start Reader (RWW Mode)
	hit, reader, err := s.Get(key)
	if err != nil {
		t.Fatalf("Get failed: err=%v", err)
	}
	if !hit {
		t.Fatalf("Get missed (expected RWW hit)")
	}
	if reader.writer == nil {
		t.Fatalf("Reader should be in RWW mode (reader.writer is nil)")
	}

	// Read all data
	readData, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if len(readData) != len(data) {
		t.Errorf("Data length mismatch: got %d, want %d", len(readData), len(data))
	} else if !bytes.Equal(data, readData) {
		t.Errorf("Data content mismatch")
	}

	// 3. Test Persistence (Post-Write Read)
	// Wait for writer to definitely close/flush
	time.Sleep(50 * time.Millisecond)

	// New Reader (Disk Mode)
	hit2, reader2, err2 := s.Get(key)
	if err2 != nil {
		t.Fatalf("Get 2 failed: %v", err2)
	}
	if !hit2 {
		t.Fatalf("Get 2 missed (expected Disk hit)")
	}
	if reader2.writer != nil {
		t.Errorf("Reader 2 should NOT be in RWW mode")
	}

	readData2, err := io.ReadAll(reader2)
	if err != nil {
		t.Fatalf("ReadAll 2 failed: %v", err)
	}
	if !bytes.Equal(data, readData2) {
		t.Errorf("Data mismatch on second read")
	}
}

func TestRWWMissing(t *testing.T) {
	path := "/tmp/bakemono-stripe-rww-missing.vol"
	os.Remove(path)
	defer os.Remove(path)

	f, _ := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	defer f.Close()
	f.Truncate(1024 * 1024 * 10)

	s := NewStripe(0, f, 0, 1024*1024*10, 0)
	s.Init(1024)

	hit, _, _ := s.Get([]byte("non-existent-key"))
	if hit {
		t.Fatal("Should be miss")
	}
}
