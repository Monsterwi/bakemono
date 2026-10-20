package cache

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/smartystreets/goconvey/convey"
)

func TestEvacuation(t *testing.T) {
	convey.Convey("Evacuation Logic", t, func() {
		tmpFile := filepath.Join(t.TempDir(), "test_stripe_evac.dat")
		f, _ := os.Create(tmpFile)
		f.Close()
		defer os.Remove(tmpFile)

		// Create a small stripe: 10MB
		// SectorSize = 512
		stripeSize := int64(10 * 1024 * 1024)
		s := NewStripe(tmpFile, stripeSize, 0, WithRamCache(0)) // Disable RamCache to force Disk IO
		s.Init(stripeSize / 512)
		s.Open()
		defer s.Close()

		// 1. Write Data A (Will be at the beginning)
		// Use 16-byte keys to avoid padding/hashing mismatches during evacuation
		keyA := []byte("0123456789abcdef")
		dataA := make([]byte, 1024*1024) // 1MB
		for i := range dataA {
			dataA[i] = 'A'
		}
		writerA := NewCacheWriter(s, keyA)
		writerA.Write(dataA)
		writerA.Close()
		s.FlushAggBufferSync() // Force flush to disk

		// Record offset of A
		_, _, dirA_Original := s.DirMgr.Get(keyA)
		convey.So(dirA_Original.offset(), convey.ShouldNotEqual, 0)

		// 2. Write Data B (Fill up most of the rest)
		// Stripe 10MB. Header+Dir ~ 100KB. A = 1MB.
		// Remaining ~ 8.9MB.
		// Write 8MB of B.
		keyB := []byte("0123456789abcdeB")
		dataB := make([]byte, 8*1024*1024) // 8MB
		for i := range dataB {
			dataB[i] = 'B'
		}
		writerB := NewCacheWriter(s, keyB)
		writerB.Write(dataB)
		writerB.Close()
		s.FlushAggBufferSync()

		// 3. Read Data A to trigger "Hit Evacuation" registration
		// Verify DirMgr still has it
		hitA, _, _ := s.DirMgr.Get(keyA)
		convey.So(hitA, convey.ShouldBeTrue)

		// This should add A to the EvacuationBucket for its offset.
		readerA := NewCacheReader(s, keyA)
		bufA := make([]byte, len(dataA))
		_, err := io.ReadFull(readerA, bufA)
		convey.So(err, convey.ShouldBeNil)
		convey.So(bytes.Equal(bufA, dataA), convey.ShouldBeTrue)

		// 4. Write Data C (Trigger Wrap Around and Overwrite A)
		// Write 2MB. 1 (A) + 8 (B) + 2 (C) = 11MB > 10MB.
		// Current WritePos should be near 9MB.
		// Writing 2MB will wrap around to 0 (Start) and overwrite A.
		keyC := []byte("0123456789abcdeC")
		dataC := make([]byte, 2*1024*1024) // 2MB
		for i := range dataC {
			dataC[i] = 'C'
		}
		writerC := NewCacheWriter(s, keyC)
		writerC.Write(dataC)
		writerC.Close()
		s.FlushAggBufferSync() // This flush should trigger evacuation of A

		// Evacuation happens asynchronously in background goroutines (triggered by Flush).
		// We need to wait a bit for them to complete (add to buffer and update directory).
		time.Sleep(1000 * time.Millisecond)

		// 5. Verify Data A is still accessible (Evacuated)
		// It should have been moved to a new offset (after C or mixed in C's flush block)
		// Check DirMgr first

		// Evacuation is async, so we might need to wait for DirMgr update
		var hit bool
		var dirA_New Dir
		for i := 0; i < 20; i++ {
			hit, _, dirA_New = s.DirMgr.Get(keyA)
			// We check if the Disk Offset (inside Dir) changed, NOT the Directory Entry Offset
			if hit && dirA_New.offset() != dirA_Original.offset() {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}

		convey.So(hit, convey.ShouldBeTrue)
		convey.So(dirA_New.offset(), convey.ShouldNotEqual, dirA_Original.offset())

		// Verify content
		readerA2 := NewCacheReader(s, keyA)
		bufA2 := make([]byte, len(dataA))
		_, err = io.ReadFull(readerA2, bufA2)
		convey.So(err, convey.ShouldBeNil)
		convey.So(bytes.Equal(bufA2, dataA), convey.ShouldBeTrue)

		// 6. Verify Data B (Might be partially overwritten or intact depending on exact boundaries)
		// A (1MB) + C (2MB) = 3MB used at start.
		// B started at ~1MB.
		// C overwrites 0..2MB.
		// So B's beginning (1MB..2MB) is overwritten by C.
		// Since B was NOT read, it should NOT have been evacuated.
		// So B should be corrupted or missing.
		// DirMgr might still point to old location, but reading checks Magic.
		readerB := NewCacheReader(s, keyB)
		bufB := make([]byte, len(dataB))
		// We expect error or corrupted data
		_, err = io.ReadFull(readerB, bufB)
		// ReadFull might fail if Magic check fails in LoadFragment
		// Or it returns EOF.
		convey.So(err, convey.ShouldNotBeNil)
	})
}

func TestRepeatedHitsRegisterOneEvacuationCandidate(t *testing.T) {
	table := NewEvacuationTable()
	for i := 0; i < 10000; i++ {
		table.Register([]byte("hot-object"), 4096, 1024)
	}
	if got := table.GetCandidatesInRange(4096, 5120); len(got) != 1 {
		t.Fatalf("same hot record registered %d times", len(got))
	}
}
