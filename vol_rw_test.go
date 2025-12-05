package bakemono

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"testing"
)

func NewTestVol() *Vol {
	return NewTestVolWithPath("/tmp/bakemono-test.vol")
}

func NewTestVolWithPath(path string) *Vol {
	cfg, err := NewDefaultVolOptions(path, 1000*1024*1024, 8000, 100)
	if err != nil {
		panic(err)
	}
	v := &Vol{}
	corrupted, err := v.Init(cfg)
	if err != nil {
		panic(err)
	}
	if corrupted {
		log.Printf("vol is corrupted, but fixed. ignore this if first time running.")
	}
	return v
}

func TestMultiFragmentLargeObject(t *testing.T) {
	path := "/tmp/bakemono-test-large.vol"
	os.Remove(path)
	v := NewTestVolWithPath(path)
	defer func() {
		v.Close()
		os.Remove(path)
	}()

	// ChunkDataSize is 4MB (4 * 1024 * 1024)
	// We create a 5MB object (Head + 1 Fragment)
	dataSize := 4*ChunkDataSize + 1024*1024 // 5MB
	data := make([]byte, dataSize)
	// Fill with pattern
	for i := range data {
		data[i] = byte((i / 1024) % 256)
	}
	key := []byte("large_obj_key")

	t.Logf("Writing large object of size %d (ChunkDataSize: %d)...", dataSize, ChunkDataSize)
	err := v.Set(key, data)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	t.Logf("Reading large object...")
	hit, reader, err := v.Get(key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !hit {
		t.Fatalf("Get miss")
	}
	val, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if len(val) != len(data) {
		t.Fatalf("Size mismatch. Expected %d, got %d", len(data), len(val))
	}

	if !bytes.Equal(val, data) {
		t.Fatalf("Data mismatch at index")
	}

	t.Logf("Large object test passed!")
}

func TestCacheReader_Seek(t *testing.T) {
	path := "/tmp/bakemono-test-seek.vol"
	os.Remove(path)
	v := NewTestVolWithPath(path)
	defer func() {
		v.Close()
		os.Remove(path)
	}()

	// 5MB object
	dataSize := 4*ChunkDataSize + 1024*1024
	data := make([]byte, dataSize)
	for i := range data {
		data[i] = byte((i / 1024) % 256)
	}
	key := []byte("seek_test_key")

	if err := v.Set(key, data); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	hit, reader, err := v.Get(key)
	if err != nil || !hit {
		t.Fatalf("Get failed")
	}

	// Seek to 1MB
	offset := int64(1024 * 1024)
	pos, err := reader.Seek(offset, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek failed: %v", err)
	}
	if pos != offset {
		t.Fatalf("Seek ret %d want %d", pos, offset)
	}

	buf := make([]byte, 1024)
	n, err := reader.Read(buf)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if n != 1024 {
		t.Fatalf("Read n=%d", n)
	}
	if !bytes.Equal(buf, data[offset:offset+1024]) {
		t.Fatalf("Data mismatch at 1MB")
	}

	// Seek to 4.5MB (across chunk boundary)
	offset = int64(4*1024*1024 + 512*1024)
	pos, err = reader.Seek(offset, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek failed: %v", err)
	}
	n, err = reader.Read(buf)
	if !bytes.Equal(buf, data[offset:offset+1024]) {
		t.Fatalf("Data mismatch at 4.5MB")
	}

	// Seek Backwards to 100 bytes
	offset = 100
	reader.Seek(offset, io.SeekStart)
	reader.Read(buf)
	if !bytes.Equal(buf, data[offset:offset+1024]) {
		t.Fatalf("Data mismatch at 100 bytes")
	}

	t.Log("Seek test passed")
}

// TestConcurrentReadWriteLargeObject tests concurrency between reading a large object and writing many small objects.
func TestConcurrentReadWriteLargeObject(t *testing.T) {
	path := "/tmp/bakemono-test-concurrent.vol"
	os.Remove(path)
	v := NewTestVolWithPath(path)
	defer func() {
		v.Close()
		os.Remove(path)
	}()

	// 1. Write a large object first (5MB)
	dataSize := ChunkDataSize + 1024*1024
	largeData := make([]byte, dataSize)
	for i := range largeData {
		largeData[i] = byte((i / 1024) % 256)
	}
	largeKey := []byte("large_obj_key")
	if err := v.Set(largeKey, largeData); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	var wg sync.WaitGroup

	// 2. Start a reader (Get) for the large object
	wg.Add(1)
	go func() {
		defer wg.Done()
		t.Log("Reader started")
		hit, reader, err := v.Get(largeKey)
		if err != nil {
			t.Errorf("Get large object failed: %v", err)
			return
		}
		if !hit {
			t.Errorf("Get large object miss")
			return
		}
		val, err := io.ReadAll(reader)
		if err != nil {
			t.Errorf("ReadAll failed: %v", err)
			return
		}
		if !bytes.Equal(val, largeData) {
			t.Errorf("Large object data corruption detected")
		}
		t.Log("Reader finished")
	}()

	// 3. Start concurrent writers (Set)
	// These should work concurrently (though blocked by RLock currently)
	wg.Add(1)
	go func() {
		defer wg.Done()
		t.Log("Writer started")
		for i := 0; i < 20; i++ {
			k := []byte(fmt.Sprintf("small_key_%d", i))
			data := []byte("small_data_content")
			if err := v.Set(k, data); err != nil {
				t.Errorf("Set small object failed: %v", err)
			}
		}
		t.Log("Writer finished")
	}()

	wg.Wait()
	t.Log("Concurrent test passed")
}

// TestConcurrent1MBFiles 测试1MB文件的并发写入和数据完整性
func TestConcurrent1MBFiles(t *testing.T) {
	os.Remove("/tmp/bakemono-test.vol")
	v := NewTestVol()
	defer func() {
		v.Close()
		os.Remove("/tmp/bakemono-test.vol")
	}()

	// 测试参数
	numGoroutines := 10
	operationsPerGoroutine := 5

	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines*operationsPerGoroutine)
	writtenKeys := make(map[string][]byte)
	var mu sync.RWMutex

	// 并发写入1MB数据
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < operationsPerGoroutine; j++ {
				key := fmt.Sprintf("test_key_%d_%d", goroutineID, j)

				// 创建1MB的唯一数据
				data := make([]byte, 1024*1024) // 1MB
				for k := range data {
					data[k] = byte((k + goroutineID*1000 + j*100) % 256)
				}

				err := v.Set([]byte(key), data)
				if err != nil {
					errors <- fmt.Errorf("Set error for key %s: %v", key, err)
				} else {
					// 记录写入的键值对用于验证
					mu.Lock()
					writtenKeys[key] = data
					mu.Unlock()
				}
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// 检查写入错误
	var writeErrorCount int
	for err := range errors {
		t.Errorf("Write error: %v", err)
		writeErrorCount++
	}

	if writeErrorCount > 0 {
		t.Fatalf("Found %d write errors", writeErrorCount)
	}

	t.Logf("Successfully completed %d concurrent 1MB Set operations",
		numGoroutines*operationsPerGoroutine)

	// 验证数据完整性
	mu.RLock()
	totalKeys := len(writtenKeys)
	mu.RUnlock()

	successCount := 0
	verifyErrorCount := 0

	mu.RLock()
	for key, expectedData := range writtenKeys {
		hit, reader, err := v.Get([]byte(key))
		if err != nil {
			t.Errorf("Get error for key %s: %v", key, err)
			verifyErrorCount++
			continue
		}
		if !hit {
			t.Errorf("Key %s not found", key)
			verifyErrorCount++
			continue
		}
		actualData, err := io.ReadAll(reader)
		if err != nil {
			t.Errorf("ReadAll error for key %s: %v", key, err)
			verifyErrorCount++
			continue
		}
		if len(actualData) != len(expectedData) {
			t.Errorf("Data length mismatch for key %s: expected %d, got %d",
				key, len(expectedData), len(actualData))
			verifyErrorCount++
			continue
		}
		if !bytes.Equal(actualData, expectedData) {
			t.Errorf("Data content mismatch for key %s", key)
			verifyErrorCount++
			continue
		}
		successCount++
	}
	mu.RUnlock()

	t.Logf("Data integrity verification: %d/%d keys verified successfully, %d errors",
		successCount, totalKeys, verifyErrorCount)

	if verifyErrorCount > 0 {
		t.Fatalf("Data integrity verification failed with %d errors", verifyErrorCount)
	}

	t.Logf("✅ All tests passed! Successfully wrote and verified %d 1MB files concurrently", totalKeys)
}
