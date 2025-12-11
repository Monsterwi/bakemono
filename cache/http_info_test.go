package cache

import (
	"testing"
	"time"

	"github.com/smartystreets/goconvey/convey"
)

func TestCacheHTTPInfo(t *testing.T) {
	convey.Convey("CacheHTTPInfo Serialization", t, func() {
		info := NewCacheHTTPInfo()
		info.RequestMethod = "GET"
		info.RequestURL = "http://example.com/foo"
		info.Status = 200
		info.RequestTime = time.Now().Round(time.Microsecond)
		info.ResponseTime = time.Now().Round(time.Microsecond)
		info.ResponseHeaders.Add("Content-Type", "text/plain")
		info.ResponseHeaders.Add("X-Cache", "HIT")

		data, err := info.MarshalBinary()
		convey.So(err, convey.ShouldBeNil)
		convey.So(len(data), convey.ShouldBeGreaterThan, 0)

		var info2 CacheHTTPInfo
		err = info2.UnmarshalBinary(data)
		convey.So(err, convey.ShouldBeNil)

		convey.So(info2.RequestMethod, convey.ShouldEqual, info.RequestMethod)
		convey.So(info2.RequestURL, convey.ShouldEqual, info.RequestURL)
		convey.So(info2.Status, convey.ShouldEqual, info.Status)
		convey.So(info2.ResponseHeaders.Get("Content-Type"), convey.ShouldEqual, "text/plain")

		// Time equality
		convey.So(info2.RequestTime.UnixNano(), convey.ShouldEqual, info.RequestTime.UnixNano())
	})
}
