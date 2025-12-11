package cache

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/smartystreets/goconvey/convey"
)

func TestCacheReadWrite(t *testing.T) {
	convey.Convey("Cache Read/Write (RWW)", t, func() {
		tmpFile := "test_stripe_rww.dat"
		f, _ := os.Create(tmpFile)
		f.Close()
		defer os.Remove(tmpFile)

		stripe := NewStripe(tmpFile, 1024*1024, 0)
		stripe.Init(1024 * 1024 / 512)
		stripe.Open()
		defer stripe.Close()

		key := []byte("test_key")

		writer := NewCacheWriter(stripe, key)
		reader := NewCacheReaderFromWriter(stripe, key, writer)

		convey.Convey("Write and Read concurrently", func() {
			data := []byte("Hello RWW World")

			// Write partial
			n, err := writer.Write(data[:5])
			convey.So(n, convey.ShouldEqual, 5)
			convey.So(err, convey.ShouldBeNil)

			// Read partial
			buf := make([]byte, 10)
			n, err = reader.Read(buf)
			convey.So(n, convey.ShouldEqual, 5)
			convey.So(err, convey.ShouldBeNil)
			convey.So(string(buf[:n]), convey.ShouldEqual, "Hello")

			// Write more
			n, err = writer.Write(data[5:])
			convey.So(n, convey.ShouldEqual, 10)
			convey.So(err, convey.ShouldBeNil)

			// Read more
			n, err = reader.Read(buf)
			convey.So(n, convey.ShouldEqual, 10)
			convey.So(err, convey.ShouldBeNil)
			convey.So(string(buf[:n]), convey.ShouldEqual, " RWW World")

			// Close writer
			writer.Close()

			// Read EOF
			n, err = reader.Read(buf)
			convey.So(n, convey.ShouldEqual, 0)
			convey.So(err, convey.ShouldEqual, io.EOF)
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

		// Verify Metadata was present (Doc loaded)
		convey.So(reader.doc, convey.ShouldNotBeNil)
	})
}

func TestCacheWriterFlush(t *testing.T) {
	convey.Convey("CacheWriter Flush", t, func() {
		stripe := NewStripe("test_stripe_flush", 1024*1024, 0)
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
		s1.FlushAggBuffer()

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
