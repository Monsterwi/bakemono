package bakemono

import (
	"bytes"
	"crypto/rand"
	"os"
	"sync"
	"testing"
	"time"
)

func TestAggregateWriteBuffer_Basic(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "agg_test_basic")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	agg := NewAggregateWriteBuffer(tmpFile)
	defer agg.Close()

	data := []byte("hello world")
	offset := int64(100) // Arbitrary offset

	// Write
	n, err := agg.WriteAt(data, offset)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(data) {
		t.Errorf("Short write: %d", n)
	}

	// Verify in memory (Active Buffer)
	readBuf := make([]byte, len(data))
	// Vol write pos should be offset + len(data)
	n, hit := agg.CopyFrom(readBuf, offset)
	if !hit || n != len(data) {
		t.Errorf("CopyFrom active buffer failed: hit=%v n=%d", hit, n)
	}
	if !bytes.Equal(readBuf, data) {
		t.Errorf("Data mismatch in memory")
	}

	// Force Flush
	// FlushSync expects the next write offset to calculate buffer start
	err = agg.FlushSync(offset + int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}

	// Verify on disk
	diskData := make([]byte, len(data))
	_, err = tmpFile.ReadAt(diskData, offset)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(diskData, data) {
		t.Errorf("Data mismatch on disk")
	}
}

func TestAggregateWriteBuffer_DoubleBuffering(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "agg_test_double")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	agg := NewAggregateWriteBuffer(tmpFile)
	defer agg.Close()

	// AggBufferSize is 4MB. We write 5MB.
	dataSize := 5 * 1024 * 1024
	data := make([]byte, dataSize)
	rand.Read(data)

	startOffset := int64(0)

	// This should trigger buffer switch
	n, err := agg.WriteAt(data, startOffset)
	if err != nil {
		t.Fatal(err)
	}
	if n != dataSize {
		t.Errorf("Short write: %d", n)
	}

	// Check Read-While-Write
	// Data should span two buffers (Pending and Active)

	// 1. Read from Pending (which contained the first 4MB)
	// pendingWritePos should be 0.
	// Try reading at offset 0, size 1KB.
	// VolWritePos is dataSize.
	buf := make([]byte, 1024)
	n, hit := agg.CopyFrom(buf, 0)
	// Note: CopyFrom checks current buffer then pending.
	// Current buffer start = 5MB - (5MB % 4MB) = 4MB.
	// Offset 0 is < 4MB, so current buffer check fails.
	// Pending buffer start = 0. Length = 4MB.
	// Offset 0 is within [0, 4MB). Should hit.

	// However, pendingActive might be false if flush completed super fast!
	// This test might be flaky if disk IO is instant.
	// But since we haven't slept, chances are high.
	// Or we can rely on the fact that we didn't wait for flush yet.

	if hit {
		if !bytes.Equal(buf, data[:1024]) {
			t.Errorf("Data mismatch from pending buffer")
		}
	} else {
		t.Log("Pending buffer flushed too fast (miss), checking disk")
		// If miss, it MUST be on disk
		diskData := make([]byte, 1024)
		_, err := tmpFile.ReadAt(diskData, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(diskData, data[:1024]) {
			t.Errorf("Data mismatch on disk (after memory miss)")
		}
	}

	// 2. Read from Active (offset 4.5MB)
	offsetActive := int64(4.5 * 1024 * 1024)
	// Active buffer start = 4MB.
	// Offset 4.5MB is within [4MB, 5MB).
	n, hit = agg.CopyFrom(buf, offsetActive)
	if !hit {
		t.Errorf("Failed to read from active buffer")
	} else if !bytes.Equal(buf, data[offsetActive:offsetActive+1024]) {
		t.Errorf("Data mismatch from active buffer")
	}

	// Force flush everything
	err = agg.FlushSync(int64(dataSize))
	if err != nil {
		t.Fatal(err)
	}

	// Verify full file on disk
	diskData := make([]byte, dataSize)
	_, err = tmpFile.ReadAt(diskData, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(diskData, data) {
		t.Errorf("Disk data mismatch")
	}
}

func TestAggregateWriteBuffer_Concurrency(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "agg_test_concurrent")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	agg := NewAggregateWriteBuffer(tmpFile)
	defer agg.Close()

	var wg sync.WaitGroup
	totalSize := 10 * 1024 * 1024 // 10MB
	chunkSize := 1024             // 1KB writes

	// Writer
	wg.Add(1)
	go func() {
		defer wg.Done()
		data := make([]byte, chunkSize)
		for i := 0; i < totalSize/chunkSize; i++ {
			// Fill pattern
			data[0] = byte(i % 256)
			diskOffset := int64(i * chunkSize)
			agg.WriteAt(data, diskOffset)
			// Yield to allow reader
			if i%100 == 0 {
				time.Sleep(time.Microsecond)
			}
		}
	}()

	// Reader (Race detector)
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 100)
		for i := 0; i < 1000; i++ {
			// Just ensure no panic/race
			agg.CopyFrom(buf, 0)
			time.Sleep(time.Microsecond)
		}
	}()

	wg.Wait()

	agg.FlushSync(int64(totalSize))

	stat, _ := tmpFile.Stat()
	if stat.Size() != int64(totalSize) {
		t.Logf("File size %d, expected %d", stat.Size(), totalSize)
	}
}
