package cache

import (
	"testing"

	"github.com/smartystreets/goconvey/convey"
)

func TestDocSerialization(t *testing.T) {
	convey.Convey("Doc Serialization", t, func() {
		d := Doc{
			Magic:       DocMagic,
			Len:         100,
			TotalLen:    200,
			FirstKey:    [16]byte{1, 2, 3},
			Key:         [16]byte{4, 5, 6},
			HLen:        10,
			DocType:     1,
			VMajor:      2,
			VMinor:      3,
			Unused:      0,
			SyncSerial:  123,
			WriteSerial: 456,
			Pinned:      789,
			Checksum:    0,
		}

		convey.Convey("MarshalBinary", func() {
			data, err := d.MarshalBinary()
			convey.So(err, convey.ShouldBeNil)
			convey.So(len(data), convey.ShouldEqual, DocHeaderSize)

			convey.Convey("UnmarshalBinary", func() {
				var d2 Doc
				err := d2.UnmarshalBinary(data)
				convey.So(err, convey.ShouldBeNil)
				convey.So(d2, convey.ShouldResemble, d)
			})
		})
	})
}

func TestDocChecksum(t *testing.T) {
	convey.Convey("Doc Checksum", t, func() {
		d := Doc{
			Len:  DocHeaderSize + 5, // 5 bytes of data
			HLen: 2,                 // 2 bytes of header
		}

		// Data after Doc struct: 2 bytes header + 3 bytes body = 5 bytes
		data := []byte{1, 2, 3, 4, 5}

		sum := d.CalculateChecksum(data)
		// 1+2+3+4+5 = 15
		convey.So(sum, convey.ShouldEqual, 15)
	})
}
