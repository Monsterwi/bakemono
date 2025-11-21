package bakemono

import (
	"bytes"
	"fmt"
	"log"
	"sync"
	"testing"
)

func NewTestVol() *Vol {
	cfg, err := NewDefaultVolOptions("/tmp/bakemono-test.vol", 1000*1024*1024, 80, 10)
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

// TestConcurrent1MBFiles 测试1MB文件的并发写入和数据完整性
func TestConcurrent1MBFiles(t *testing.T) {
	v := NewTestVol()
	defer v.Close()

	// 测试参数
	numGoroutines := 100
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
		hit, actualData, err := v.Get([]byte(key))
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
