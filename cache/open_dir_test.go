package cache

import (
	"testing"

	"github.com/smartystreets/goconvey/convey"
)

func TestOpenDirConcurrency(t *testing.T) {
	convey.Convey("OpenDir Concurrency Control", t, func() {
		od := NewOpenDir()
		key := []byte("test_key")
		w1 := &CacheWriter{Key: key} // Mock writer
		w2 := &CacheWriter{Key: key} // Mock writer

		// 1. First writer succeeds
		ok := od.OpenWrite(key, w1)
		convey.So(ok, convey.ShouldBeTrue)

		// 2. Second writer fails (Single Writer Policy)
		ok = od.OpenWrite(key, w2)
		convey.So(ok, convey.ShouldBeFalse)

		// 3. Read finds the writer
		entry := od.OpenRead(key)
		convey.So(entry, convey.ShouldNotBeNil)
		entry.mutex.RLock()
		convey.So(len(entry.writers), convey.ShouldEqual, 1)
		convey.So(entry.writers[0], convey.ShouldEqual, w1)
		entry.mutex.RUnlock()

		// 4. Close first writer
		od.CloseWrite(key, w1)

		// 5. Entry should be removed
		entry = od.OpenRead(key)
		convey.So(entry, convey.ShouldBeNil)

		// 6. Second writer can now succeed
		ok = od.OpenWrite(key, w2)
		convey.So(ok, convey.ShouldBeTrue)
	})
}
