package cache

import (
	"bytes"
	"io"
	"os"
	"testing"
	"time"

	"github.com/smartystreets/goconvey/convey"
)

func TestCacheReadWrite(t *testing.T) {
	convey.Convey("Cache Read/Write (RWW)", t, func() {
		tmpFile := "test_stripe_rww.dat"
		f, _ := os.Create(tmpFile)
		f.Close()
		defer os.Remove(tmpFile)

		// 100MB Stripe
		stripe := NewStripe(tmpFile, 100*1024*1024, 0)
		stripe.Init(100 * 1024 * 1024 / 512)
		stripe.Open()
		defer stripe.Close()

		convey.Convey("Small Object (Single Fragment)", func() {
			key := []byte("small_key")
			writer := NewCacheWriter(stripe, key)
			reader := NewCacheReaderFromWriter(stripe, key, writer)

			data := []byte("Small Data < 1MB")

			// Async Writer
			go func() {
				time.Sleep(10 * time.Millisecond)
				writer.Write(data)
				writer.Close()
			}()

			buf := make([]byte, len(data))
			n, err := io.ReadFull(reader, buf)

			convey.So(err, convey.ShouldBeNil)
			convey.So(n, convey.ShouldEqual, len(data))
			convey.So(string(buf), convey.ShouldEqual, string(data))
		})

		convey.Convey("Large Object (Multi-Fragment)", func() {
			key := []byte("large_key")
			writer := NewCacheWriter(stripe, key)
			reader := NewCacheReaderFromWriter(stripe, key, writer)

			// 2.5MB Data (TargetFragmentSize is 1MB)
			// This forces the writer to flush Frag 1 and Frag 2 to disk/ramcache
			// while reader is reading.
			dataSize := int(2.5 * 1024 * 1024)
			data := make([]byte, dataSize)
			for i := 0; i < dataSize; i++ {
				data[i] = byte(i % 256)
			}

			// Async Writer
			go func() {
				chunkSize := 512 * 1024 // 512KB writes
				totalWritten := 0
				for i := 0; i < dataSize; i += chunkSize {
					end := i + chunkSize
					if end > dataSize {
						end = dataSize
					}
					writer.Write(data[i:end])
					totalWritten += (end - i)

					// Small delay to allow reader to catch up or fall behind
					time.Sleep(5 * time.Millisecond)
				}
				writer.Close()
			}()

			readBuf := make([]byte, dataSize)
			n, err := io.ReadFull(reader, readBuf)

			// Reader might see ErrReadFromDisk if the writer flushed part of the buffer before reader could read it from RAM.
			// However, since we are mocking a Stripe with a file, io.ReadFull should eventually work if logic falls back to disk.
			// BUT, the test uses io.ReadFull, which returns EOF if stream ends.
			// With RWW, reader waits for writer.
			// If writer flushes, Reader gets ErrReadFromDisk from `ReadAt`.
			// The updated `readFromWriter` handles this by loading fragment from disk.
			// So io.ReadFull should succeed transparently.

			convey.So(err, convey.ShouldBeNil)
			convey.So(n, convey.ShouldEqual, dataSize)
			convey.So(bytes.Equal(readBuf, data), convey.ShouldBeTrue)
		})
	})
}

func TestCacheDiskPersistence(t *testing.T) {
	convey.Convey("Cache Disk Persistence", t, func() {
		tmpFile := "test_stripe_disk.dat"
		f, _ := os.Create(tmpFile)
		f.Close()
		defer os.Remove(tmpFile)

		// Use small stripe for test (but large enough for metadata)
		// Metadata: Header(24) + Dir(~8K buckets * 10 = 80KB).
		// Let's use 1MB.
		stripe := NewStripe(tmpFile, 1024*1024, 0)
		stripe.Init(1024 * 1024 / 512)
		stripe.Open()
		defer stripe.Close()

		key := []byte("persist_key")
		data := []byte("Persisted Data")

		// Write
		writer := NewCacheWriter(stripe, key)
		writer.Write(data)
		err := writer.Close()
		convey.So(err, convey.ShouldBeNil)

		// Flush Stripe AggBuffer to Disk
		err = stripe.FlushAggBuffer()
		convey.So(err, convey.ShouldBeNil)

		// Read from Disk (create new reader)
		reader := NewCacheReader(stripe, key)
		buf := make([]byte, 50)

		// Verify HTTPInfo loading (implicit in Read)
		n, err := reader.Read(buf)

		convey.So(err, convey.ShouldBeNil)
		convey.So(n, convey.ShouldEqual, len(data))
		convey.So(string(buf[:n]), convey.ShouldEqual, string(data))

		// Verify Metadata was present (buffer should have data)
		convey.So(len(reader.buf), convey.ShouldBeGreaterThan, 0)
	})
}

func TestCacheWriterFlush(t *testing.T) {
	convey.Convey("CacheWriter Flush", t, func() {
		stripe := NewStripe("test_stripe_flush", 1024*1024, 0)
		defer os.Remove("test_stripe_flush")
		stripe.Init(1024 * 1024 / 512)
		stripe.Open() // Need to open to have Fd
		defer stripe.Close()
		key := []byte("flush_key")
		writer := NewCacheWriter(stripe, key)

		data := []byte("Flush Me")
		writer.Write(data)

		err := writer.Close()
		convey.So(err, convey.ShouldBeNil)

		// Check AggBuffer
		convey.So(stripe.AggBuffer.IsEmpty(), convey.ShouldBeFalse)
		// bytesPendingAggregation was subtracted in Add.
		// Since we started with 0 and didn't AddBytesPendingAggregation, it might be negative if logic is strictly following ATS.
		// ATS Logic: bufferPos increases, bytesPending decreases.
		// If we assume bytesPending tracks what is WAITING to be put in buffer, then adding to buffer reduces it.
		// Let's just check buffer usage.
		convey.So(stripe.AggBuffer.GetBufferPos(), convey.ShouldBeGreaterThan, 0)
	})
}

func TestStripePersistence(t *testing.T) {
	convey.Convey("Stripe Header Persistence", t, func() {
		tmpFile := "test_stripe_persist.dat"
		f, _ := os.Create(tmpFile)
		f.Close()
		defer os.Remove(tmpFile)

		// 1. Create and Init
		s1 := NewStripe(tmpFile, 1024*1024, 0)
		s1.Init(1024 * 1024 / 512)
		err := s1.Open()
		convey.So(err, convey.ShouldBeNil)

		// Modify state (WritePos) by writing something
		startPos := s1.WritePos
		key := []byte("persist_key")
		writer := NewCacheWriter(s1, key)
		writer.Write([]byte("data"))
		writer.Close()
		err = s1.FlushAggBufferSync() // Use sync flush to ensure WritePos is updated
		convey.So(err, convey.ShouldBeNil)

		newPos := s1.WritePos
		convey.So(newPos, convey.ShouldBeGreaterThan, startPos)

		// Close (triggers Save)
		err = s1.Close()
		convey.So(err, convey.ShouldBeNil)

		// 2. Reopen
		s2 := NewStripe(tmpFile, 1024*1024, 0)
		s2.Init(1024 * 1024 / 512)
		err = s2.Open() // Should Load
		convey.So(err, convey.ShouldBeNil)
		defer s2.Close()

		// Verify state restored
		convey.So(s2.WritePos, convey.ShouldEqual, newPos)

		// Verify Dir entry exists
		hit, _, _ := s2.DirMgr.Get(key)
		convey.So(hit, convey.ShouldBeTrue)
	})
}

func TestCacheRamCacheIntegration(t *testing.T) {
	convey.Convey("Cache RamCache Integration", t, func() {
		tmpFile := "test_stripe_ram.dat"
		f, _ := os.Create(tmpFile)
		f.Close()
		defer os.Remove(tmpFile)

		stripe := NewStripe(tmpFile, 1024*1024, 0)
		stripe.Init(1024 * 1024 / 512)
		stripe.Open()
		defer stripe.Close()

		key := []byte("ram_key")
		data := []byte("Ram Data")

		// 1. Write (should populate RamCache)
		writer := NewCacheWriter(stripe, key)
		writer.Write(data)
		err := writer.Close()
		convey.So(err, convey.ShouldBeNil)

		// Verify RamCache has it
		val, ok := stripe.RamCache.Get(key)
		convey.So(ok, convey.ShouldBeTrue)
		convey.So(len(val), convey.ShouldBeGreaterThan, len(data)) // Header + Info + Data

		// 2. Read (should hit RamCache)
		reader := NewCacheReader(stripe, key)
		buf := make([]byte, 50)
		n, err := reader.Read(buf)

		convey.So(err, convey.ShouldBeNil)
		convey.So(n, convey.ShouldEqual, len(data))
		convey.So(string(buf[:n]), convey.ShouldEqual, string(data))

		// Verify internal state implies RamCache usage (offset should be advanced, but fd not necessarily used)
		// Difficult to verify "no disk IO" without mocks, but correctness is verified.

		// 3. Verify Metadata from RamCache
		info, err := reader.LoadHTTPInfo()
		convey.So(err, convey.ShouldBeNil)
		convey.So(info, convey.ShouldNotBeNil)
	})
}

func TestCacheMultiFragment(t *testing.T) {
	convey.Convey("Cache Multi-Fragment Read/Write", t, func() {
		// Use a larger file size to accommodate multiple 1MB fragments
		tmpFile := "test_stripe_multi.dat"
		fileSize := int64(10 * 1024 * 1024) // 10MB
		f, _ := os.Create(tmpFile)
		f.Close()
		defer os.Remove(tmpFile)

		// Initialize stripe WITH RamCache disabled (size 0) to force disk I/O logic check
		// Or we can use it enabled and rely on the fact that we write to disk too.
		stripe := NewStripe(tmpFile, fileSize, 0, WithRamCache(0))
		stripe.Init(fileSize / 512)
		stripe.Open()
		defer stripe.Close()

		key := []byte("multi_frag_key")

		// Create data > 1MB (TargetFragmentSize)
		// TargetFragmentSize is 1MB. Let's use 2.5MB.
		dataSize := int(2.5 * 1024 * 1024)
		data := make([]byte, dataSize)
		// Fill with pattern
		for i := 0; i < dataSize; i++ {
			data[i] = byte(i % 256)
		}

		// 1. Write
		writer := NewCacheWriter(stripe, key)

		// Write in chunks to test streaming buffer logic
		chunk := 1024 * 512 // 512KB
		for i := 0; i < dataSize; i += chunk {
			end := i + chunk
			if end > dataSize {
				end = dataSize
			}
			_, err := writer.Write(data[i:end])
			convey.So(err, convey.ShouldBeNil)
		}

		err := writer.Close()
		convey.So(err, convey.ShouldBeNil)

		// Flush to disk to ensure data is persistent
		err = stripe.FlushAggBuffer()
		convey.So(err, convey.ShouldBeNil)

		// 2. Read (RamCache disabled, so MUST read from disk)
		reader := NewCacheReader(stripe, key)
		readBuf := make([]byte, dataSize)

		// Read full data
		// The Read loop should switch fragments transparently.
		n, err := io.ReadFull(reader, readBuf)
		convey.So(err, convey.ShouldBeNil)
		convey.So(n, convey.ShouldEqual, dataSize)
		convey.So(bytes.Equal(readBuf, data), convey.ShouldBeTrue)
	})
}

func TestCacheRemove(t *testing.T) {
	convey.Convey("Cache Remove (PURGE)", t, func() {
		tmpFile := "test_stripe_remove.dat"
		f, _ := os.Create(tmpFile)
		f.Close()
		defer os.Remove(tmpFile)

		stripe := NewStripe(tmpFile, 1024*1024, 0)
		stripe.Init(1024 * 1024 / 512)
		stripe.Open()
		defer stripe.Close()

		key := []byte("purge_key")
		data := []byte("Purge Me")

		// 1. Write data
		writer := NewCacheWriter(stripe, key)
		writer.Write(data)
		err := writer.Close()
		convey.So(err, convey.ShouldBeNil)

		// Verify it's in the directory
		hit, _, _ := stripe.DirMgr.Get(key)
		convey.So(hit, convey.ShouldBeTrue)

		// Verify it's in RamCache
		_, ok := stripe.RamCache.Get(key)
		convey.So(ok, convey.ShouldBeTrue)

		// 2. Remove (PURGE)
		cache := &Cache{Stripes: []*Stripe{stripe}}
		err = cache.Remove(key)
		convey.So(err, convey.ShouldBeNil)

		// 3. Verify it's gone from Directory
		hit, _, _ = stripe.DirMgr.Get(key)
		convey.So(hit, convey.ShouldBeFalse)

		// 4. Verify it's gone from RamCache
		_, ok = stripe.RamCache.Get(key)
		convey.So(ok, convey.ShouldBeFalse)

		// 5. Verify it's gone from OpenDir (already tested via success of Remove if we had concurrent write, but let's be explicit if we can)
		// Since we closed the writer, it shouldn't be in OpenDir anyway.
		// Let's test with an active writer.

		key2 := []byte("purge_key_active")
		vc2, _ := NewCacheVCWrite(stripe, key2)
		vc2.Write(data)
		// vc2 is still open, so it's in OpenDir

		convey.So(stripe.OpenDir.Get(key2), convey.ShouldNotBeNil)

		cache2 := &Cache{Stripes: []*Stripe{stripe}}
		err = cache2.Remove(key2)
		convey.So(err, convey.ShouldBeNil)

		convey.So(stripe.OpenDir.Get(key2), convey.ShouldBeNil)
		vc2.Close()
	})

	convey.Convey("Cache Remove (PURGE) without RamCache", t, func() {
		tmpFile := "test_stripe_remove_no_ram.dat"
		f, _ := os.Create(tmpFile)
		f.Close()
		defer os.Remove(tmpFile)

		// Disable RamCache
		stripe := NewStripe(tmpFile, 1024*1024, 0, WithRamCache(0))
		stripe.Init(1024 * 1024 / 512)
		stripe.Open()
		defer stripe.Close()

		convey.So(stripe.RamCache, convey.ShouldBeNil)

		key := []byte("purge_key_no_ram")
		data := []byte("Purge Me No Ram")

		// 1. Write data
		writer := NewCacheWriter(stripe, key)
		writer.Write(data)
		err := writer.Close()
		convey.So(err, convey.ShouldBeNil)

		// Verify it's in the directory
		hit, _, _ := stripe.DirMgr.Get(key)
		convey.So(hit, convey.ShouldBeTrue)

		// 2. Remove (PURGE)
		cache := &Cache{Stripes: []*Stripe{stripe}}
		err = cache.Remove(key)
		convey.So(err, convey.ShouldBeNil)

		// 3. Verify it's gone from Directory
		hit, _, _ = stripe.DirMgr.Get(key)
		convey.So(hit, convey.ShouldBeFalse)
	})

	convey.Convey("Cache Remove (PURGE) Multi-Fragment", t, func() {
		tmpFile := "test_stripe_remove_multifrag.dat"
		fileSize := int64(10 * 1024 * 1024) // 10MB
		f, _ := os.Create(tmpFile)
		f.Close()
		defer os.Remove(tmpFile)

		stripe := NewStripe(tmpFile, fileSize, 0)
		stripe.Init(fileSize / 512)
		stripe.Open()
		defer stripe.Close()

		key := []byte("purge_key_multifrag")

		// Create data larger than TargetFragmentSize (1MB)
		// This will create multiple fragments
		dataSize := 2*1024*1024 + 512*1024 // 2.5MB - should create 3 fragments
		data := make([]byte, dataSize)
		for i := 0; i < dataSize; i++ {
			data[i] = byte(i % 256)
		}

		// 1. Write multi-fragment data
		writer := NewCacheWriter(stripe, key)
		n, err := writer.Write(data)
		convey.So(err, convey.ShouldBeNil)
		convey.So(n, convey.ShouldEqual, len(data))
		err = writer.Close()
		convey.So(err, convey.ShouldBeNil)

		// Verify fragment 0 is in the directory
		hit, _, _ := stripe.DirMgr.Get(key)
		convey.So(hit, convey.ShouldBeTrue)

		// Verify fragment 1 is in the directory
		frag1Key := FragmentKey(key, 1)
		hit, _, _ = stripe.DirMgr.Get(frag1Key)
		convey.So(hit, convey.ShouldBeTrue)

		// Verify fragment 2 is in the directory
		frag2Key := FragmentKey(key, 2)
		hit, _, _ = stripe.DirMgr.Get(frag2Key)
		convey.So(hit, convey.ShouldBeTrue)

		// 2. Remove (PURGE) - should remove all fragments
		cache := &Cache{Stripes: []*Stripe{stripe}}
		err = cache.Remove(key)
		convey.So(err, convey.ShouldBeNil)

		// 3. Verify all fragments are gone from Directory
		hit, _, _ = stripe.DirMgr.Get(key)
		convey.So(hit, convey.ShouldBeFalse)

		hit, _, _ = stripe.DirMgr.Get(frag1Key)
		convey.So(hit, convey.ShouldBeFalse)

		hit, _, _ = stripe.DirMgr.Get(frag2Key)
		convey.So(hit, convey.ShouldBeFalse)
	})
}
