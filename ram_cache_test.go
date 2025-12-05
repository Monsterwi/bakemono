package bakemono

import (
	"testing"
)

func TestNewRamCache(t *testing.T) {
	tests := []struct {
		name    string
		entries uint64
	}{
		{"small cache", 10},
		{"medium cache", 1000},
		{"large cache", 10000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := NewRamCache(tt.entries)
			if cache == nil {
				t.Fatal("NewRamCacheLRU returned nil")
			}
			if cache.Cache == nil {
				t.Fatal("cache.Cache is nil")
			}
		})
	}
}

func TestRamCacheOtter_Put(t *testing.T) {
	cache := NewRamCache(100)

	tests := []struct {
		name  string
		key   []byte
		value []byte
	}{
		{"simple put", []byte("key1"), []byte("value1")},
		{"empty value", []byte("key2"), []byte("")},
		{"large value", []byte("key3"), make([]byte, 1024)},
		{"unicode key", []byte("键4"), []byte("值4")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := cache.Put(tt.key, tt.value)
			if err != nil {
				t.Errorf("Put() error = %v, want nil", err)
			}
		})
	}
}

func TestRamCacheOtter_Get(t *testing.T) {
	cache := NewRamCache(100)

	// Put some test data
	testData := map[string][]byte{
		"key1": []byte("value1"),
		"key2": []byte("value2"),
		"key3": []byte(""),
	}

	for k, v := range testData {
		err := cache.Put([]byte(k), v)
		if err != nil {
			t.Fatalf("Failed to put test data: %v", err)
		}
	}

	tests := []struct {
		name      string
		key       []byte
		wantValue []byte
		wantFound bool
	}{
		{"existing key", []byte("key1"), []byte("value1"), true},
		{"existing key with empty value", []byte("key3"), []byte(""), true},
		{"non-existing key", []byte("nonexistent"), nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, found := cache.Get(tt.key)

			if found != tt.wantFound {
				t.Errorf("Get() found = %v, want %v", found, tt.wantFound)
			}

			if tt.wantFound {
				if got == nil {
					t.Errorf("Get() = nil, want %v", tt.wantValue)
					return
				}
				if string(got) != string(tt.wantValue) {
					t.Errorf("Get() = %v, want %v", got, tt.wantValue)
				}
			} else {
				if got != nil {
					t.Errorf("Get() = %v, want nil", got)
				}
			}
		})
	}
}

func TestRamCacheOtter_Size(t *testing.T) {
	cache := NewRamCache(100)

	// Initially empty
	if items := cache.Items(); items != 0 {
		t.Errorf("Items() = %d, want 0", items)
	}

	// Add some entries
	for i := 0; i < 5; i++ {
		key := []byte("key" + string(rune('0'+i)))
		value := []byte("value" + string(rune('0'+i)))
		err := cache.Put(key, value)
		if err != nil {
			t.Fatalf("Put() error = %v", err)
		}
	}

	cache.CleanUp()

	items := cache.Items()
	if items != 5 {
		t.Errorf("Items() = %d, want 5", items)
	}
}

func TestRamCacheOtter_Overwrite(t *testing.T) {
	cache := NewRamCache(100)

	key := []byte("testkey")
	value1 := []byte("value1")
	value2 := []byte("value2")

	// Put initial value
	err := cache.Put(key, value1)
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	// Verify initial value
	got, found := cache.Get(key)
	if !found {
		t.Fatalf("Get() not found")
	}
	if string(got) != string(value1) {
		t.Errorf("Get() = %v, want %v", got, value1)
	}

	// Overwrite with new value
	err = cache.Put(key, value2)
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	// Verify new value
	got, found = cache.Get(key)
	if !found {
		t.Fatalf("Get() not found")
	}
	if string(got) != string(value2) {
		t.Errorf("Get() = %v, want %v", got, value2)
	}

	cache.CleanUp()

	// Size should still be 1
	if items := cache.Items(); items != 1 {
		t.Errorf("Items() = %d, want 1", items)
	}
}

func TestRamCacheOtter_Eviction(t *testing.T) {
	// Create a small cache that will trigger eviction
	cache := NewRamCache(3)

	// Fill the cache
	for i := 0; i < 3; i++ {
		key := []byte("key" + string(rune('0'+i)))
		value := []byte("value" + string(rune('0'+i)))
		err := cache.Put(key, value)
		if err != nil {
			t.Fatalf("Put() error = %v", err)
		}
	}

	// Verify all entries are present
	for i := 0; i < 3; i++ {
		key := []byte("key" + string(rune('0'+i)))
		got, found := cache.Get(key)
		if !found {
			t.Fatalf("Get() not found for %s", key)
		}
		if got == nil {
			t.Errorf("Get(%s) = nil, want value", key)
		}
	}

	// Add one more entry to trigger eviction
	err := cache.Put([]byte("key3"), []byte("value3"))
	cache.CleanUp()
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	// Cache size should not exceed maximum
	cache.CleanUp()
	items := cache.Items()
	if items > 3 {
		t.Errorf("Items() = %d, want <= 3", items)
	}
}

func TestRamCacheOtter_LargeData(t *testing.T) {
	cache := NewRamCache(10)

	// Test with large data
	largeData := make([]byte, 64*1024) // 64KB
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	key := []byte("large_key")
	err := cache.Put(key, largeData)
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}

	got, found := cache.Get(key)
	if !found {
		t.Fatalf("Get() not found")
	}

	if len(got) != len(largeData) {
		t.Errorf("Get() length = %d, want %d", len(got), len(largeData))
	}

	// Verify data integrity
	for i := 0; i < len(largeData); i++ {
		if got[i] != largeData[i] {
			t.Errorf("Data mismatch at index %d: got %d, want %d", i, got[i], largeData[i])
			break
		}
	}
}

func BenchmarkRamCacheOtter_Put(b *testing.B) {
	cache := NewRamCache(10000)
	key := []byte("benchmark_key")
	value := []byte("benchmark_value")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Put(key, value)
		cache.CleanUp()
	}
}

func BenchmarkRamCacheOtter_Get(b *testing.B) {
	cache := NewRamCache(10000)
	key := []byte("benchmark_key")
	value := []byte("benchmark_value")
	cache.Put(key, value)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cache.Get(key)
		cache.CleanUp()
		cache.Get(key)
	}
}

func BenchmarkRamCacheOtter_PutGet(b *testing.B) {
	cache := NewRamCache(10000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := []byte("key" + string(rune(i%1000)))
		value := []byte("value" + string(rune(i%1000)))
		cache.Put(key, value)
		cache.Get(key)
	}
}
