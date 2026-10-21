package cache

import (
	"fmt"
	"runtime"
	"testing"
)

// BenchmarkRAMTrace replays identical deterministic traces and byte budgets.
// It measures the RAM policy seam; disk latency and HTTP P99 belong to acceptance.
func BenchmarkRAMTrace(b *testing.B) {
	const capacity = 10 << 20
	for _, algorithm := range []string{"otter", "s3-fifo"} {
		for _, size := range []int{256, 256 << 10, 1 << 20, 4 << 20} {
			for _, twiceRatio := range []int{1, 2, 8} {
				for _, trace := range []string{"scan", "hot-scan", "hot-switch", "read-write"} {
					name := fmt.Sprintf("%s/bytes=%d/workset=%g/trace=%s", algorithm, size, float64(twiceRatio)/2, trace)
					b.Run(name, func(b *testing.B) {
						c, err := NewRamCacheWithAlgorithm(capacity, algorithm)
						if err != nil {
							b.Fatal(err)
						}
						value := make([]byte, size)
						keys := make([][]byte, max(1, capacity*twiceRatio/(2*size)))
						for i := range keys {
							keys[i] = []byte(fmt.Sprintf("object-%d", i))
						}
						for _, key := range keys[:min(len(keys), capacity/size)] {
							c.Put(key, value)
						}
						c.CleanUp()
						var before, after runtime.MemStats
						runtime.ReadMemStats(&before)
						hits := 0
						peak := c.Size()
						hot := max(1, len(keys)/16)
						b.ReportAllocs()
						b.SetBytes(int64(size))
						b.ResetTimer()
						for i := 0; i < b.N; i++ {
							index := i % len(keys)
							if trace == "hot-scan" && i%5 != 0 {
								index = i % hot
							}
							if trace == "hot-switch" {
								index = i % hot
								if (i/1024)%2 == 1 {
									index = len(keys) - hot + index
								}
							}
							key := keys[index]
							if _, ok := c.Get(key); ok {
								hits++
							} else {
								c.Put(key, value)
							}
							if trace == "read-write" && i%4 == 0 {
								c.Put(key, value)
							}
							if trace == "read-write" && i%97 == 0 {
								c.Delete(key)
							}
							if i%1024 == 0 {
								peak = max(peak, c.Size())
							}
						}
						b.StopTimer()
						runtime.ReadMemStats(&after)
						// Capture the concurrent-policy snapshot before CleanUp, then settled state.
						st := c.Stats()
						peak = max(peak, st.ResidentBytes)
						c.CleanUp()
						settled := c.Stats()
						b.ReportMetric(float64(hits)/float64(max(1, b.N)), "hit/req")
						b.ReportMetric(float64(hits*size)/float64(max(1, b.N*size)), "byte-hit/byte")
						b.ReportMetric(float64(peak), "peak-resident-B")
						b.ReportMetric(float64(settled.ResidentBytes), "resident-B")
						b.ReportMetric(float64(settled.GhostEntries), "ghost-entries")
						b.ReportMetric(float64(settled.EstimatedMetadataBytes), "metadata-est-B")
						b.ReportMetric(float64(after.NumGC-before.NumGC), "GCs")
					})
				}
			}
		}
	}
}
