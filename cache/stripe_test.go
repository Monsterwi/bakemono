package cache

import (
	"os"
	"testing"

	"github.com/smartystreets/goconvey/convey"
)

func TestStripe(t *testing.T) {
	convey.Convey("Stripe", t, func() {
		tmpFile := "test_stripe.dat"
		f, _ := os.Create(tmpFile)
		f.Close()
		defer os.Remove(tmpFile)

		s := NewStripe(tmpFile, 1024*1024*10, 0) // 10MB
		convey.So(s, convey.ShouldNotBeNil)

		err := s.Init(1024 * 1024 * 10 / 512)
		convey.So(err, convey.ShouldBeNil)

		err = s.Open()
		convey.So(err, convey.ShouldBeNil)

		convey.So(s.Fd, convey.ShouldNotBeNil)

		err = s.Close()
		convey.So(err, convey.ShouldBeNil)
	})
}

func TestStripeSaveLoad(t *testing.T) {
	convey.Convey("Stripe Persistence", t, func() {
		tmpFile := "test_stripe_persistence.dat"
		defer os.Remove(tmpFile)

		// Test 1: Save and Load empty stripe
		convey.Convey("Save and Load empty stripe", func() {
			s1 := NewStripe(tmpFile, 1024*1024*10, 0, WithRamCache(0))
			err := s1.Init(1024 * 1024 * 10 / 512)
			convey.So(err, convey.ShouldBeNil)

			err = s1.Open()
			convey.So(err, convey.ShouldBeNil)

			// Save initial state
			err = s1.Save()
			convey.So(err, convey.ShouldBeNil)

			// Close and reopen
			err = s1.Close()
			convey.So(err, convey.ShouldBeNil)

			s2 := NewStripe(tmpFile, 1024*1024*10, 0, WithRamCache(0))
			err = s2.Init(1024 * 1024 * 10 / 512)
			convey.So(err, convey.ShouldBeNil)

			err = s2.Open()
			convey.So(err, convey.ShouldBeNil)

			// Verify WritePos and Phase are restored
			convey.So(s2.WritePos, convey.ShouldEqual, s1.WritePos)
			convey.So(s2.Phase, convey.ShouldEqual, s1.Phase)
			convey.So(s2.Header, convey.ShouldNotBeNil)
			convey.So(s2.Header.Magic, convey.ShouldEqual, StripeHeaderMagic)

			err = s2.Close()
			convey.So(err, convey.ShouldBeNil)
		})

		// Test 2: Save and Load with WritePos and Phase changes
		convey.Convey("Save and Load with WritePos and Phase changes", func() {
			s1 := NewStripe(tmpFile, 1024*1024*10, 0, WithRamCache(0))
			err := s1.Init(1024 * 1024 * 10 / 512)
			convey.So(err, convey.ShouldBeNil)

			err = s1.Open()
			convey.So(err, convey.ShouldBeNil)

			// Modify WritePos and Phase
			originalWritePos := s1.WritePos
			s1.WritePos += 1024 * 100 // Advance WritePos
			s1.Phase = true           // Change Phase

			// Save state
			err = s1.Save()
			convey.So(err, convey.ShouldBeNil)

			err = s1.Close()
			convey.So(err, convey.ShouldBeNil)

			// Reopen
			s2 := NewStripe(tmpFile, 1024*1024*10, 0, WithRamCache(0))
			err = s2.Init(1024 * 1024 * 10 / 512)
			convey.So(err, convey.ShouldBeNil)

			err = s2.Open()
			convey.So(err, convey.ShouldBeNil)

			// Verify WritePos and Phase are restored
			convey.So(s2.WritePos, convey.ShouldEqual, s1.WritePos)
			convey.So(s2.WritePos, convey.ShouldNotEqual, originalWritePos)
			convey.So(s2.Phase, convey.ShouldEqual, true)
			convey.So(s2.Phase, convey.ShouldEqual, s1.Phase)

			err = s2.Close()
			convey.So(err, convey.ShouldBeNil)
		})

		// Test 3: Save and Load with Directory entries
		convey.Convey("Save and Load with Directory entries", func() {
			s1 := NewStripe(tmpFile, 1024*1024*10, 0, WithRamCache(0))
			err := s1.Init(1024 * 1024 * 10 / 512)
			convey.So(err, convey.ShouldBeNil)

			err = s1.Open()
			convey.So(err, convey.ShouldBeNil)

			// Write data using CacheWriter to get real disk offsets
			key1 := []byte("test_key_1")
			key2 := []byte("test_key_2")
			key3 := []byte("test_key_3")

			// Write first entry
			writer1 := NewCacheWriter(s1, key1)
			writer1.Write([]byte("data1"))
			writer1.Close()
			err = s1.FlushAggBuffer()
			convey.So(err, convey.ShouldBeNil)

			// Write second entry
			writer2 := NewCacheWriter(s1, key2)
			writer2.Write([]byte("data2"))
			writer2.Close()
			err = s1.FlushAggBuffer()
			convey.So(err, convey.ShouldBeNil)

			// Write third entry
			writer3 := NewCacheWriter(s1, key3)
			writer3.Write([]byte("data3"))
			writer3.Close()
			err = s1.FlushAggBuffer()
			convey.So(err, convey.ShouldBeNil)

			// Verify entries exist (they should be set by CacheWriter.Close)
			hit1, dirOffset1, dir1 := s1.DirMgr.Get(key1)
			convey.So(hit1, convey.ShouldBeTrue)
			convey.So(dirOffset1, convey.ShouldNotEqual, Offset(0))
			convey.So(dir1.offset(), convey.ShouldNotEqual, Offset(0))

			hit2, dirOffset2, dir2 := s1.DirMgr.Get(key2)
			convey.So(hit2, convey.ShouldBeTrue)
			convey.So(dirOffset2, convey.ShouldNotEqual, Offset(0))

			hit3, dirOffset3, dir3 := s1.DirMgr.Get(key3)
			convey.So(hit3, convey.ShouldBeTrue)
			convey.So(dirOffset3, convey.ShouldNotEqual, Offset(0))

			// Save state
			err = s1.Save()
			convey.So(err, convey.ShouldBeNil)

			err = s1.Close()
			convey.So(err, convey.ShouldBeNil)

			// Reopen
			s2 := NewStripe(tmpFile, 1024*1024*10, 0, WithRamCache(0))
			err = s2.Init(1024 * 1024 * 10 / 512)
			convey.So(err, convey.ShouldBeNil)

			err = s2.Open()
			convey.So(err, convey.ShouldBeNil)

			// Verify directory entries are restored with correct offsets
			hit1Reloaded, dirOffset1Reloaded, dir1Reloaded := s2.DirMgr.Get(key1)
			convey.So(hit1Reloaded, convey.ShouldBeTrue)
			convey.So(dirOffset1Reloaded, convey.ShouldEqual, dirOffset1)
			convey.So(dir1Reloaded.offset(), convey.ShouldEqual, dir1.offset())

			hit2Reloaded, dirOffset2Reloaded, dir2Reloaded := s2.DirMgr.Get(key2)
			convey.So(hit2Reloaded, convey.ShouldBeTrue)
			convey.So(dirOffset2Reloaded, convey.ShouldEqual, dirOffset2)
			convey.So(dir2Reloaded.offset(), convey.ShouldEqual, dir2.offset())

			hit3Reloaded, dirOffset3Reloaded, dir3Reloaded := s2.DirMgr.Get(key3)
			convey.So(hit3Reloaded, convey.ShouldBeTrue)
			convey.So(dirOffset3Reloaded, convey.ShouldEqual, dirOffset3)
			convey.So(dir3Reloaded.offset(), convey.ShouldEqual, dir3.offset())

			err = s2.Close()
			convey.So(err, convey.ShouldBeNil)
		})

		// Test 4: Full persistence test with data write and read
		convey.Convey("Full persistence test with data write and read", func() {
			s1 := NewStripe(tmpFile, 1024*1024*10, 0, WithRamCache(0))
			err := s1.Init(1024 * 1024 * 10 / 512)
			convey.So(err, convey.ShouldBeNil)

			err = s1.Open()
			convey.So(err, convey.ShouldBeNil)

			// Write test data
			testKey := []byte("/test/persistence")
			writer := NewCacheWriter(s1, testKey)

			info := NewCacheHTTPInfo()
			info.Status = 200
			info.RequestURL = "/test/persistence"
			writer.Info = info

			testData := []byte("Hello, Persistence Test!")
			_, err = writer.Write(testData)
			convey.So(err, convey.ShouldBeNil)

			err = writer.Close()
			convey.So(err, convey.ShouldBeNil)

			// Flush and save
			err = s1.FlushAggBuffer()
			convey.So(err, convey.ShouldBeNil)

			err = s1.Save()
			convey.So(err, convey.ShouldBeNil)

			err = s1.Close()
			convey.So(err, convey.ShouldBeNil)

			// Reopen
			s2 := NewStripe(tmpFile, 1024*1024*10, 0, WithRamCache(0))
			err = s2.Init(1024 * 1024 * 10 / 512)
			convey.So(err, convey.ShouldBeNil)

			err = s2.Open()
			convey.So(err, convey.ShouldBeNil)

			// Verify WritePos is restored
			convey.So(s2.WritePos, convey.ShouldEqual, s1.WritePos)

			// Try to read the data
			reader := NewCacheReader(s2, testKey)
			infoReloaded, err := reader.LoadHTTPInfo()
			convey.So(err, convey.ShouldBeNil)
			convey.So(infoReloaded, convey.ShouldNotBeNil)
			convey.So(infoReloaded.Status, convey.ShouldEqual, 200)
			convey.So(infoReloaded.RequestURL, convey.ShouldEqual, "/test/persistence")

			// Read data
			readData := make([]byte, len(testData))
			n, err := reader.Read(readData)
			convey.So(err, convey.ShouldBeNil)
			convey.So(n, convey.ShouldEqual, len(testData))
			convey.So(string(readData), convey.ShouldEqual, string(testData))

			err = s2.Close()
			convey.So(err, convey.ShouldBeNil)
		})

		// Test 5: Multiple save/load cycles
		convey.Convey("Multiple save/load cycles", func() {
			s := NewStripe(tmpFile, 1024*1024*10, 0, WithRamCache(0))
			err := s.Init(1024 * 1024 * 10 / 512)
			convey.So(err, convey.ShouldBeNil)

			// Multiple cycles
			for i := 0; i < 3; i++ {
				err = s.Open()
				convey.So(err, convey.ShouldBeNil)

				// Modify state
				s.WritePos += int64(1024 * (i + 1))
				s.Phase = i%2 == 0

				// Add directory entry
				key := []byte{byte('a' + i)}
				_, err = s.DirMgr.Set(key, Offset(s.WritePos), 1024)
				convey.So(err, convey.ShouldBeNil)

				// Save
				err = s.Save()
				convey.So(err, convey.ShouldBeNil)

				err = s.Close()
				convey.So(err, convey.ShouldBeNil)

				// Reopen
				s = NewStripe(tmpFile, 1024*1024*10, 0, WithRamCache(0))
				err = s.Init(1024 * 1024 * 10 / 512)
				convey.So(err, convey.ShouldBeNil)
			}

			// Final verification
			err = s.Open()
			convey.So(err, convey.ShouldBeNil)

			// Verify all entries exist
			for i := 0; i < 3; i++ {
				key := []byte{byte('a' + i)}
				hit, _, dir := s.DirMgr.Get(key)
				convey.So(hit, convey.ShouldBeTrue)
				convey.So(dir.offset(), convey.ShouldNotEqual, Offset(0))
			}

			err = s.Close()
			convey.So(err, convey.ShouldBeNil)
		})
	})
}
