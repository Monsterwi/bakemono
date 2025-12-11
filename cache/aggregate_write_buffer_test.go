package cache

import (
	"testing"

	"github.com/smartystreets/goconvey/convey"
)

type mockWriter struct {
	flushed bool
	err     error
}

func (m *mockWriter) OnLogFlush(err error) {
	m.flushed = true
	m.err = err
}

type mockDisk struct {
	data []byte
}

func (m *mockDisk) WriteAt(p []byte, off int64) (n int, err error) {
	if int(off)+len(p) > len(m.data) {
		newSize := int(off) + len(p)
		newData := make([]byte, newSize)
		copy(newData, m.data)
		m.data = newData
	}
	copy(m.data[off:], p)
	return len(p), nil
}

func TestAggregateWriteBuffer(t *testing.T) {
	convey.Convey("AggregateWriteBuffer", t, func() {
		buf := NewAggregateWriteBuffer()
		convey.So(buf.IsEmpty(), convey.ShouldBeTrue)

		data := []byte("hello world")
		writer := &mockWriter{}

		err := buf.Add(data, 100, writer)
		convey.So(err, convey.ShouldBeNil)
		convey.So(buf.IsEmpty(), convey.ShouldBeFalse)
		convey.So(buf.GetBufferPos(), convey.ShouldEqual, len(data))

		disk := &mockDisk{}
		err = buf.Flush(disk, 0)
		convey.So(err, convey.ShouldBeNil)
		convey.So(writer.flushed, convey.ShouldBeTrue)
		convey.So(string(disk.data), convey.ShouldEqual, "hello world")

		buf.Reset()
		convey.So(buf.IsEmpty(), convey.ShouldBeTrue)
	})
}
