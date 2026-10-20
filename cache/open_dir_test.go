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

		// 1. First writer succeeds (Leader)
		entry1, isLeader := od.Add(key, w1)
		convey.So(isLeader, convey.ShouldBeTrue)
		convey.So(entry1, convey.ShouldNotBeNil)

		// 2. Second writer fails without acquiring a reference
		entry2, isLeader := od.Add(key, w2)
		convey.So(isLeader, convey.ShouldBeFalse)
		convey.So(entry2, convey.ShouldEqual, entry1) // Should return same entry

		// 3. Read finds the entry
		entry := od.Get(key)
		convey.So(entry, convey.ShouldNotBeNil)
		convey.So(entry.Writer, convey.ShouldEqual, w1)

		// 4. Remove reference (Simulate Close)
		od.Remove(key) // Writer close

		// 5. Entry should be removed ONLY if RefCount is 0
		// Only the successful Add owned a reference.
		entry = od.Get(key)
		convey.So(entry, convey.ShouldBeNil)

		// 6. Second writer can now succeed
		entry, isLeader = od.Add(key, w2)
		convey.So(isLeader, convey.ShouldBeTrue)
	})
}
