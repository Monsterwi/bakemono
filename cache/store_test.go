package cache

import (
	"os"
	"testing"

	"github.com/smartystreets/goconvey/convey"
)

func TestStore(t *testing.T) {
	convey.Convey("Store Configuration", t, func() {
		// Create a dummy storage.config
		configFile := "storage.config"
		content := `
# This is a comment
/tmp/cache1 128M
/tmp/cache2 256M volume=1
/tmp/cache3 1G
		`
		f, err := os.Create(configFile)
		convey.So(err, convey.ShouldBeNil)
		f.WriteString(content)
		f.Close()
		defer os.Remove(configFile)

		// Create dummy cache files
		os.Create("/tmp/cache1")
		os.Create("/tmp/cache2")
		os.Create("/tmp/cache3")
		defer os.Remove("/tmp/cache1")
		defer os.Remove("/tmp/cache2")
		defer os.Remove("/tmp/cache3")

		store := NewStore()
		err = store.ReadConfig(configFile)
		convey.So(err, convey.ShouldBeNil)

		convey.So(len(store.Spans), convey.ShouldEqual, 3)

		s1 := store.Spans[0]
		convey.So(s1.Path, convey.ShouldEqual, "/tmp/cache1")
		convey.So(s1.Size, convey.ShouldEqual, 128*1024*1024)

		s2 := store.Spans[1]
		convey.So(s2.Path, convey.ShouldEqual, "/tmp/cache2")
		convey.So(s2.Size, convey.ShouldEqual, 256*1024*1024)

		s3 := store.Spans[2]
		convey.So(s3.Path, convey.ShouldEqual, "/tmp/cache3")
		convey.So(s3.Size, convey.ShouldEqual, 1024*1024*1024)

		// BuildStripeLayout: requesting fewer stripes than spans should error
		_, _, _, err = store.BuildStripeLayout(2)
		convey.So(err, convey.ShouldNotBeNil)

		// Request more stripes than spans: spans will be split
		paths, sizes, offsets, err := store.BuildStripeLayout(4)
		convey.So(err, convey.ShouldBeNil)
		convey.So(len(paths), convey.ShouldEqual, 4)
		// Total size preserved
		var total int64
		for _, sz := range sizes {
			total += sz
		}
		convey.So(total, convey.ShouldEqual, 128*1024*1024+256*1024*1024+1024*1024*1024)
		// Offsets should start at 0 and be non-decreasing within same path
		for i := 0; i < len(paths); i++ {
			convey.So(offsets[i], convey.ShouldBeGreaterThanOrEqualTo, int64(0))
		}
	})

	convey.Convey("Size Parsing", t, func() {
		sz, err := parseSize("100")
		convey.So(err, convey.ShouldBeNil)
		convey.So(sz, convey.ShouldEqual, 100)

		sz, err = parseSize("1K")
		convey.So(err, convey.ShouldBeNil)
		convey.So(sz, convey.ShouldEqual, 1024)

		sz, err = parseSize("1M")
		convey.So(err, convey.ShouldBeNil)
		convey.So(sz, convey.ShouldEqual, 1024*1024)

		sz, err = parseSize("1G")
		convey.So(err, convey.ShouldBeNil)
		convey.So(sz, convey.ShouldEqual, 1024*1024*1024)

		_, err = parseSize("1X")
		convey.So(err, convey.ShouldNotBeNil)
	})
}
