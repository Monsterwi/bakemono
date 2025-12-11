package cache

import (
	"os"
	"testing"
	"time"

	"github.com/smartystreets/goconvey/convey"
)

func TestStripeSM(t *testing.T) {
	convey.Convey("StripeSM Background Flush", t, func() {
		tmpFile := "test_stripe_sm.dat"
		f, _ := os.Create(tmpFile)
		f.Close()
		defer os.Remove(tmpFile)

		stripe := NewStripe(tmpFile, 1024*1024, 0)
		stripe.Init(1024 * 1024 / 512)

		// Reduce flush interval for test
		stripe.SM.FlushInterval = 50 * time.Millisecond

		err := stripe.Open()
		convey.So(err, convey.ShouldBeNil)
		defer stripe.Close()

		key := []byte("sm_key")
		data := []byte("data")

		// Write without explicit close/flush
		// We manually add to AggBuffer to simulate pending write
		// (CacheWriter usually handles this, but let's go direct)

		// Wait, CacheWriter ADDS to AggBuffer.
		// But FlushAggBuffer is what StripeSM calls.
		// So if we use CacheWriter and Close it, it puts data in AggBuffer.
		// Then we wait for SM to flush it to disk.

		writer := NewCacheWriter(stripe, key)
		writer.Write(data)
		writer.Close() // This puts data in AggBuffer

		// Verify buffer is NOT empty
		stripe.mu.RLock()
		pos := stripe.AggBuffer.GetBufferPos()
		stripe.mu.RUnlock()
		convey.So(pos, convey.ShouldBeGreaterThan, 0)

		// Wait for background flush
		time.Sleep(150 * time.Millisecond)

		// Verify buffer IS empty
		stripe.mu.RLock()
		posAfter := stripe.AggBuffer.GetBufferPos()
		stripe.mu.RUnlock()
		convey.So(posAfter, convey.ShouldEqual, 0)

		// Verify data on disk
		reader := NewCacheReader(stripe, key)
		buf := make([]byte, 10)
		n, err := reader.Read(buf)
		convey.So(err, convey.ShouldBeNil)
		convey.So(n, convey.ShouldEqual, len(data))
	})
}

func TestCacheVC(t *testing.T) {
	convey.Convey("CacheVC Lifecycle", t, func() {
		tmpFile := "test_cache_vc.dat"
		f, _ := os.Create(tmpFile)
		f.Close()
		defer os.Remove(tmpFile)

		stripe := NewStripe(tmpFile, 1024*1024, 0)
		stripe.Init(1024 * 1024 / 512)
		stripe.Open()
		defer stripe.Close()

		key := []byte("vc_key")
		data := []byte("VC Data")

		// 1. Write via VC
		writeVC, err := NewCacheVCWrite(stripe, key)
		convey.So(err, convey.ShouldBeNil)

		info := NewCacheHTTPInfo()
		info.Status = 200
		writeVC.SetHTTPInfo(info)

		n, err := writeVC.Write(data)
		convey.So(n, convey.ShouldEqual, len(data))
		convey.So(err, convey.ShouldBeNil)

		err = writeVC.Close()
		convey.So(err, convey.ShouldBeNil)

		// Flush disk
		stripe.FlushAggBuffer()

		// 2. Read via VC
		readVC, err := NewCacheVCRead(stripe, key)
		convey.So(err, convey.ShouldBeNil)

		readInfo, err := readVC.GetHTTPInfo()
		convey.So(err, convey.ShouldBeNil)
		convey.So(readInfo.Status, convey.ShouldEqual, 200)

		buf := make([]byte, 20)
		n, err = readVC.Read(buf)
		convey.So(n, convey.ShouldEqual, len(data))
		convey.So(string(buf[:n]), convey.ShouldEqual, string(data))

		readVC.Close()
	})
}
