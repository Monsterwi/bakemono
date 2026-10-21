package server_test

import (
	"bytes"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestUnsafeOrdinaryResponseInvalidatesPublicRangeManifest(t *testing.T) {
	for _, policy := range []string{"private, max-age=3600", "no-store"} {
		t.Run(policy, func(t *testing.T) {
			var unsafe atomic.Bool
			old := bytes.Repeat([]byte("a"), 2<<20)
			newer := bytes.Repeat([]byte("b"), 2<<20)
			s, _ := publicCacheServer(t, func(w http.ResponseWriter, r *http.Request) {
				body, tag, cacheControl := old, `"one"`, "public, max-age=3600"
				if unsafe.Load() {
					body, tag, cacheControl = newer, `"two"`, policy
				}
				w.Header().Set("ETag", tag)
				w.Header().Set("Cache-Control", cacheControl)
				http.ServeContent(w, r, "body", time.Time{}, bytes.NewReader(body))
			})
			target := "http://cache.test/item"
			headers := http.Header{"Range": {"bytes=0-16"}, "Accept-Encoding": {"identity"}}
			first := fetch(s, target, headers)
			if first.Code != 206 || !bytes.Equal(first.Body.Bytes(), old[:17]) {
				t.Fatalf("warm: %d %q", first.Code, first.Body.Bytes())
			}
			unsafe.Store(true)
			// A conditional ordinary GET follows the existing revalidation path.
			changed := fetch(s, target, http.Header{"If-None-Match": {`"other"`}, "Accept-Encoding": {"identity"}, "Cache-Control": {"no-cache"}})
			if changed.Code != 200 || !bytes.Equal(changed.Body.Bytes(), newer) {
				t.Fatalf("ordinary unsafe: %d length=%d", changed.Code, changed.Body.Len())
			}
			got := fetch(s, target, headers)
			if got.Code != 206 || !bytes.Equal(got.Body.Bytes(), newer[:17]) {
				t.Fatalf("unsafe ordinary response left old shared Range reachable: %d %q", got.Code, got.Body.Bytes())
			}
		})
	}
}
