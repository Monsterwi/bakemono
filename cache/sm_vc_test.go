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

		data := []byte("background bytes")
		flushed := make(flushResult, 1)
		offset, err := stripe.AggBuffer.Add(data, len(data), flushed)
		convey.So(err, convey.ShouldBeNil)
		select {
		case err := <-flushed:
			convey.So(err, convey.ShouldBeNil)
		case <-time.After(time.Second):
			t.Fatal("maintenance did not flush queued bytes")
		}
		got := make([]byte, len(data))
		_, err = stripe.Fd.ReadAt(got, offset)
		convey.So(err, convey.ShouldBeNil)
		convey.So(got, convey.ShouldResemble, data)
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

		// Manually remove from OpenDir to simulate that writing is truly done
		// In real app, close doesn't auto-remove from OpenDir if we want to keep it for slow readers?
		// But here we want to test disk read, so we must ensure NewCacheVCRead doesn't pick up RWW.
		stripe.OpenDir.Remove(key)
		// Actually, NewCacheVCWrite does OpenDir.Add.
		// writeVC.Close() does NOT call OpenDir.Remove in our new implementation (it relies on CloseWrite or manual).
		// Wait, Close() calls writeFragment(0).
		// If we want to test disk read, we need to ensure OpenDir entry is gone or we force disk read.
		// NewCacheVCRead prefers RWW if entry exists.

		// Flush disk to be sure
		stripe.FlushAggBuffer()

		// 2. Read via VC
		readVC, err := NewCacheVCRead(stripe, key)
		convey.So(err, convey.ShouldBeNil)

		readInfo, err := readVC.LoadHTTPInfo()
		convey.So(err, convey.ShouldBeNil)
		convey.So(readInfo.Status, convey.ShouldEqual, 200)

		buf := make([]byte, 20)
		n, err = readVC.Read(buf)
		convey.So(n, convey.ShouldEqual, len(data))
		convey.So(string(buf[:n]), convey.ShouldEqual, string(data))

		readVC.Close()
	})
}

type flushResult chan error

func (f flushResult) OnLogFlush(err error) { f <- err }
