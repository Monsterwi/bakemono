package cache

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func metadataObject(t *testing.T, algorithm string) (*Cache, *Stripe, []byte) {
	t.Helper()
	c, stripe := openStorageTest(t, filepath.Join(t.TempDir(), "metadata"), 32<<20, 0, 4<<20)
	var err error
	stripe.RamCache, err = NewRamCacheWithAlgorithm(4<<20, algorithm)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("metadata")
	w, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	info := &CacheHTTPInfo{RequestTime: now, ResponseTime: now, RequestMethod: "GET", RequestURL: "http://cache.test/item", Status: 200, ResponseHeaders: http.Header{"Cache-Control": {"public, max-age=3600"}, "ETag": {"v1"}}}
	for i := 0; i < 12; i++ {
		info.ResponseHeaders.Add(fmt.Sprintf("X-Field-%d", i), "original")
	}
	w.SetHTTPInfo(info)
	if _, err = w.Write([]byte("body")); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	return c, stripe, key
}

func TestRAMHTTPMetadataIsIsolatedAndCharged(t *testing.T) {
	for _, algorithm := range []string{"otter", "s3-fifo"} {
		t.Run(algorithm, func(t *testing.T) {
			c, stripe, key := metadataObject(t, algorithm)
			raw, ok := stripe.RamCache.Get(key)
			if !ok {
				t.Fatal("fixture not in RAM")
			}
			stripe.RamCache.CleanUp()
			if stripe.RamCache.Size() <= uint64(len(raw)) {
				t.Fatal("decoded metadata is not charged to RAM budget")
			}
			first, err := c.OpenReadCommitted(key)
			if err != nil {
				t.Fatal(err)
			}
			info, err := first.LoadHTTPInfo()
			if err != nil {
				t.Fatal(err)
			}
			info.ResponseHeaders.Set("ETag", "client-mutation")
			info.ResponseHeaders["X-Field-0"][0] = "client-mutation"
			first.Close()
			next, err := c.OpenReadCommitted(key)
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close()
			info, err = next.LoadHTTPInfo()
			if err != nil {
				t.Fatal(err)
			}
			if info.ResponseHeaders.Get("ETag") != "v1" || info.ResponseHeaders.Get("X-Field-0") != "original" {
				t.Fatal("reader mutated shared HTTP metadata")
			}
			body, err := io.ReadAll(next)
			if err != nil || string(body) != "body" {
				t.Fatalf("body %q: %v", body, err)
			}
			// Even a warm metadata entry must validate its backing document on every open.
			raw[len(raw)-1] ^= 1
			damaged, err := c.OpenReadCommitted(key)
			if err != nil {
				t.Fatal(err)
			}
			defer damaged.Close()
			if _, err = damaged.LoadHTTPInfo(); err == nil {
				t.Fatal("warm metadata hid a corrupt document")
			}
		})
	}
}

func TestWarmHTTPMetadataReadAllocationBudget(t *testing.T) {
	c, _, key := metadataObject(t, "otter")
	allocs := testing.AllocsPerRun(50, func() {
		r, err := c.OpenReadCommitted(key)
		if err != nil {
			t.Fatal(err)
		}
		info, err := r.LoadHTTPInfo()
		if err != nil {
			t.Fatal(err)
		}
		if info.Status != 200 {
			t.Fatal(info.Status)
		}
		r.Close()
	})
	t.Logf("warm metadata open: %.0f allocations", allocs)
	if allocs > 32 {
		t.Fatalf("warm metadata open uses %.0f allocations; budget is 32", allocs)
	}
}

func TestCachedHTTPMetadataTracksReplacementAndEviction(t *testing.T) {
	for _, algorithm := range []string{"otter", "s3-fifo"} {
		t.Run(algorithm, func(t *testing.T) {
			c, stripe, key := metadataObject(t, algorithm)
			first, err := c.OpenReadCommitted(key)
			if err != nil {
				t.Fatal(err)
			}
			old, err := first.LoadHTTPInfo()
			if err != nil {
				t.Fatal(err)
			}
			first.Close()
			replacement := old.clone()
			replacement.ResponseHeaders.Set("ETag", "v2")
			w, err := c.OpenWrite(key, 0)
			if err != nil {
				t.Fatal(err)
			}
			w.SetHTTPInfo(replacement)
			if _, err = w.Write([]byte("replacement")); err != nil {
				t.Fatal(err)
			}
			if err = w.Close(); err != nil {
				t.Fatal(err)
			}
			for _, diskOnly := range []bool{false, true} {
				if diskOnly {
					stripe.RamCache.Delete(key)
				}
				r, err := c.OpenReadCommitted(key)
				if err != nil {
					t.Fatal(err)
				}
				info, err := r.LoadHTTPInfo()
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(r)
				r.Close()
				if err != nil || info.ResponseHeaders.Get("ETag") != "v2" || string(body) != "replacement" {
					t.Fatalf("stale metadata disk=%v: %v %q %v", diskOnly, info.ResponseHeaders, body, err)
				}
			}
			if _, err = c.PurgeSelector(key, false); err != nil {
				t.Fatal(err)
			}
			r, err := c.OpenReadCommitted(key)
			if err == nil {
				defer r.Close()
				if _, err = r.LoadHTTPInfo(); err == nil {
					t.Fatal("purged metadata resurrected")
				}
			}
		})
	}
}
