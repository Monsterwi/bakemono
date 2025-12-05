package bakemono

import (
	"os"
	"testing"
)

// TestStripeMetaCorruption tests the dual-buffer metadata persistence (Meta A/Meta B).
// It simulates corruption scenarios where one meta is invalid (torn write or corrupted).
func TestStripeMetaCorruption(t *testing.T) {
	path := "/tmp/bakemono-stripe-corruption.vol"
	stripeSize := int64(1024 * 1024 * 128) // 128MB
	chunkSize := uint64(1024 * 1024)

	os.Remove(path)
	defer os.Remove(path)

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(stripeSize); err != nil {
		t.Fatal(err)
	}

	// Helper to create and init stripe
	createStripe := func() *Stripe {
		s := NewStripe(0, f, 0, stripeSize, 0)
		corrupted, err := s.Init(chunkSize)
		if corrupted {
			t.Logf("stripe is corrupted, err: %v", err)
		}
		return s
	}

	// 1. Initialize and write Serial 1(A), 2(B), 3(A)
	// Note: Init writes Serial 1 (A).
	{
		s := createStripe() // Serial 0 default
		s.Set([]byte("k1"), []byte("v1"))
		s.flushMetaToFp() // Serial 1 (A). Contains k1.
		s.Set([]byte("k2"), []byte("v2"))
		// s.FlushMeta() -> We want the next flush to be A.
		// s.Close() calls FlushMeta.
		// So Close -> Serial 2 (B). Contains k1, k2.
		s.Close()
	}

	// State:
	// Meta A: Serial 1. Contains k1, k2.
	// Meta B: Serial 2. Contains k1.

	// Calculate offsets (based on Stripe Init logic)
	// We can access them from a recovered stripe instance
	sDummy := createStripe()
	headerAOffset := int64(sDummy.HeaderAOffset)
	headerBOffset := int64(sDummy.HeaderBOffset)

	corrupt := func(off int64) {
		f.WriteAt(make([]byte, HeaderSize), off)
		f.Sync()
	}

	t.Run("CorruptA_FallbackB", func(t *testing.T) {
		// Corrupt A (Latest, Serial 1). Should fallback to B (Serial 2).
		// k2, k1 should be preserved.
		corrupt(headerAOffset)

		s := NewStripe(0, f, 0, stripeSize, 0)
		// Manually trigger recovery by Init
		corrupted, err := s.Init(chunkSize)
		if corrupted {
			t.Logf("stripe is corrupted, err: %v", err)
		}

		// k2 was in Meta B (valid), so it should be found
		if found, _, _ := s.Get([]byte("k2")); !found {
			t.Fatal("k2 should be found (B valid)")
		}
		// k1 was in Meta B (valid), so it should be found
		if found, _, _ := s.Get([]byte("k1")); !found {
			t.Fatal("k1 should be found (B valid)")
		}
		// Check Serial
		if s.Header.SyncSerial != 2 {
			t.Fatalf("Expected Serial 2, got %d", s.Header.SyncSerial)
		}
	})

	t.Run("CorruptB_FallbackA", func(t *testing.T) {
		// Reset state for this test.
		f.Truncate(0)
		{
			s := createStripe() // Init -> 0
			s.Set([]byte("k1"), []byte("v1"))
			s.flushMetaToFp() // 1(A) -> k1
			s.Set([]byte("k2"), []byte("v2"))
			s.Close() // 2(B) -> k1, k2
		}

		// Current state: A=1(k1), B=2(k1, k2).
		// Corrupt B (Old, Serial 2).
		// Should still use A.
		corrupt(headerBOffset)

		s := NewStripe(0, f, 0, stripeSize, 0)
		corrupted, err := s.Init(chunkSize)
		if corrupted {
			t.Logf("stripe is corrupted, err: %v", err)
		}

		if found, _, _ := s.Get([]byte("k2")); found {
			t.Fatal("k2 should be lost (B corrupted)")
		}
		if found, _, _ := s.Get([]byte("k1")); !found {
			t.Fatal("k1 should be found (A valid)")
		}
		if s.Header.SyncSerial != 1 {
			t.Fatalf("Expected Serial 1, got %d", s.Header.SyncSerial)
		}
	})
}
