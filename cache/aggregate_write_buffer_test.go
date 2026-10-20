package cache

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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

func TestAggregateWriteBuffer(t *testing.T) {
	convey.Convey("AggregateWriteBuffer", t, func() {
		buf := NewAggregateWriteBuffer()
		buf.Init(1000) // Start offset 1000
		convey.So(buf.IsEmpty(), convey.ShouldBeTrue)

		data := []byte("hello world")
		writer := &mockWriter{}

		offset, err := buf.Add(data, 100, writer)
		convey.So(err, convey.ShouldBeNil)
		convey.So(offset, convey.ShouldEqual, 1000)
		convey.So(buf.IsEmpty(), convey.ShouldBeFalse)
		convey.So(buf.GetBufferPos(), convey.ShouldEqual, len(data))
		convey.So(buf.GetWriteCursor(), convey.ShouldEqual, 1000+int64(len(data)))

		// Test Flush Task Generation
		// We consume the channel manually to simulate background worker
		go func() {
			buf.FlushSync()
		}()

		select {
		case task := <-buf.GetFlushChan():
			convey.So(string(task.Buffer), convey.ShouldEqual, "hello world")
			convey.So(len(task.Writers), convey.ShouldEqual, 1)

			// Simulate finish
			buf.NotifyFlushDone()
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Flush task not received")
		}

		convey.So(buf.IsEmpty(), convey.ShouldBeTrue)
	})
}

func TestAggregateWriteBuffer_DoubleBuffering(t *testing.T) {
	convey.Convey("Double Buffering", t, func() {
		buf := NewAggregateWriteBuffer()
		buf.Init(0)

		// 1. Add data to fill first buffer partially
		data1 := make([]byte, AggBufferSize/2) // 2MB
		offset1, err := buf.Add(data1, len(data1), nil)
		convey.So(err, convey.ShouldBeNil)
		convey.So(offset1, convey.ShouldEqual, 0)

		// Add reached the high-water mark and queued the first buffer.
		// Receiving its task establishes the ordering before writing buffer 2.
		var task1 FlushTask
		select {
		case task1 = <-buf.GetFlushChan():
			convey.So(len(task1.Buffer), convey.ShouldEqual, len(data1))
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Timeout waiting for task 1")
		}

		// Now buf is in "flushing" state (buffer 0 is flushing)
		// We can write to buffer 1

		// 4. Add data to second buffer
		data2 := make([]byte, AggBufferSize/4) // 1MB
		offset2, err := buf.Add(data2, len(data2), nil)
		convey.So(err, convey.ShouldBeNil)
		convey.So(offset2, convey.ShouldEqual, int64(len(data1))) // Offset continues
		convey.So(buf.GetBufferPos(), convey.ShouldEqual, len(data2))

		// 5. Simulate Task 1 completion
		buf.NotifyFlushDone()

		// 6. Now check that buffer 1 data is still there
		// Trigger flush for buffer 1
		convey.So(buf.GetBufferPos(), convey.ShouldEqual, len(data2))
		flushDone := make(chan error, 1)
		go func() {
			flushDone <- buf.FlushSync()
		}()

		select {
		case task2 := <-buf.GetFlushChan():
			convey.So(len(task2.Buffer), convey.ShouldEqual, len(data2))
			buf.NotifyFlushDone()
		case <-time.After(100 * time.Millisecond):
			t.Fatal("Timeout waiting for task 2")
		}
		select {
		case err := <-flushDone:
			convey.So(err, convey.ShouldBeNil)
		case <-time.After(time.Second):
			t.Fatal("FlushSync did not return after both buffers completed")
		}
	})
}

func TestAggregateWriteBuffer_ConcurrentWrites(t *testing.T) {
	convey.Convey("Concurrent Writes", t, func() {
		buf := NewAggregateWriteBuffer()
		buf.Init(0)

		// Start background flusher (simulates slow disk)
		stopFlusher := make(chan bool)
		flusherDone := make(chan bool)
		go func() {
			defer func() { flusherDone <- true }()
			for {
				select {
				case <-buf.GetFlushChan():
					time.Sleep(5 * time.Millisecond) // Simulate disk latency
					buf.NotifyFlushDone()
				case <-stopFlusher:
					return
				}
			}
		}()

		// Concurrent writes from 10 goroutines
		const numWriters = 10
		const writesPerWriter = 50
		const dataSize = 1024 // 1KB per write

		var wg sync.WaitGroup
		errChan := make(chan error, numWriters)

		for i := 0; i < numWriters; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for j := 0; j < writesPerWriter; j++ {
					data := make([]byte, dataSize)
					data[0] = byte(id) // Mark with writer ID
					_, err := buf.Add(data, dataSize, nil)
					if err != nil {
						errChan <- err
						return
					}
				}
			}(i)
		}

		wg.Wait()
		close(errChan)

		// Wait for pending flushes to complete
		time.Sleep(50 * time.Millisecond)
		close(stopFlusher)
		<-flusherDone

		// Verify no errors
		for err := range errChan {
			convey.So(err, convey.ShouldBeNil)
		}

		// Verify total bytes written
		expectedBytes := int64(numWriters * writesPerWriter * dataSize)
		convey.So(buf.GetWriteCursor(), convey.ShouldEqual, expectedBytes)
	})
}

func BenchmarkAggregateWriteBuffer_Add(b *testing.B) {
	buf := NewAggregateWriteBuffer()
	buf.Init(0)
	data := make([]byte, 1024) // 1KB

	// Start a consumer to drain flush channel
	go func() {
		for range buf.GetFlushChan() {
			buf.NotifyFlushDone()
		}
	}()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Add(data, 1024, nil)
	}
}

func TestAggregateFlushKeepsInFlightBufferUntilAcknowledged(t *testing.T) {
	b := NewAggregateWriteBuffer()
	b.Init(1024)
	first := bytes.Repeat([]byte{'A'}, AggHighWaterMark)
	if _, err := b.Add(first, len(first), nil); err != nil {
		t.Fatal(err)
	}
	task1 := <-b.GetFlushChan()
	if _, err := b.Add([]byte("second"), 6, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.FlushContext(ctx) }()
	// A cancelled barrier must leave ownership of task1 with its consumer.
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("flush: %v", err)
	}
	if _, err := b.Add([]byte("third"), 5, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(task1.Buffer, first) {
		t.Fatal("in-flight bytes reused before acknowledgement")
	}
	b.NotifyFlushDone()
	go func() { done <- b.FlushSync() }()
	task2 := <-b.GetFlushChan()
	if string(task2.Buffer) != "secondthird" {
		t.Fatalf("remaining bytes: %q", task2.Buffer)
	}
	b.NotifyFlushDone()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAggregateRejectsImpossibleWritesAndCancelsBackpressure(t *testing.T) {
	b := NewAggregateWriteBuffer()
	b.Init(1024)
	b.SetBounds(1024, 2048)
	if _, err := b.Add(make([]byte, 1025), 1025, nil); err == nil {
		t.Fatal("oversized record accepted")
	}
	if _, err := b.Add(make([]byte, AggBufferSize+1), AggBufferSize+1, nil); err == nil {
		t.Fatal("record larger than buffer accepted")
	}
	if _, err := b.Add(make([]byte, 1024), 1024, nil); err != nil {
		t.Fatal(err)
	}
	b.TriggerFlush()
	task := <-b.GetFlushChan()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.AddContext(ctx, []byte("next"), 4, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("AddContext=%v", err)
	}
	if task.Offset != 1024 || task.Generation != 0 {
		t.Fatalf("allocation=%+v", task)
	}
	b.NotifyFlushDone()
	if _, err := b.Add([]byte("next"), 4, nil); err != nil {
		t.Fatal(err)
	}
	b.TriggerFlush()
	wrapped := <-b.GetFlushChan()
	if wrapped.Offset != 1024 || wrapped.Generation != 1 {
		t.Fatalf("wrapped allocation=%+v", wrapped)
	}
	b.NotifyFlushDone()
}
