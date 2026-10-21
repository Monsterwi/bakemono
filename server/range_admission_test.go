package server_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Monsterwi/razor/cache"
	"github.com/Monsterwi/razor/config"
	"github.com/Monsterwi/razor/proxy"
	"github.com/Monsterwi/razor/server"
)

type slowRangeTrigger struct {
	done chan struct{}
	once sync.Once
}

func TestS3ConcurrentSlowRangeSurvivesRepeatedDiskWrap(t *testing.T) {
	const total int64 = 100 << 20
	var trigger atomic.Pointer[slowRangeTrigger]
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		begin, end, status := int64(0), total-1, http.StatusOK
		if value := r.Header.Get("Range"); value != "" {
			if _, err := fmt.Sscanf(value, "bytes=%d-%d", &begin, &end); err != nil {
				t.Error(err)
				return
			}
			status = http.StatusPartialContent
		}
		w.Header().Set("ETag", `"large-v1"`)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.FormatInt(end-begin+1, 10))
		if status == 206 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", begin, end, total))
		}
		w.WriteHeader(status)
		if r.Method == "HEAD" {
			return
		}
		source := io.NewSectionReader(repeatedRangeSource{total}, begin, end-begin+1)
		buf := make([]byte, 64<<10)
		for {
			n, err := source.Read(buf)
			if n > 0 {
				if _, writeErr := w.Write(buf[:n]); writeErr != nil {
					return
				}
				if r.URL.Path == "/large-soak-slow" {
					if begin == 2<<20 {
						if remaining, _ := source.Seek(0, io.SeekCurrent); remaining == source.Size() {
							if gate := trigger.Load(); gate != nil {
								gate.once.Do(func() { close(gate.done) })
							}
						}
					}
					time.Sleep(time.Millisecond)
				}
			}
			if err != nil {
				return
			}
		}
	}))
	defer origin.Close()
	cfg := config.GetDefaultConfig()
	cfg.SharedFillPublicStatic = 1
	cfg.RangeBlockBytes = 1 << 20
	cfg.MaxCacheObjectBytes = 256 << 20
	cfg.RAMCacheAlgorithm = "s3-fifo"
	cfg.PurgeToken = "test"
	option, err := cache.WithRAMPolicy("s3-fifo", 10<<20)
	if err != nil {
		t.Fatal(err)
	}
	c, err := cache.NewCacheWithOffsets(1, []string{filepath.Join(t.TempDir(), "cache")}, []int64{32 << 20}, nil, option)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Init(); err != nil {
		t.Fatal(err)
	}
	cfg.UpstreamTargets = []string{origin.URL}
	cfg.UpstreamAlgo = "round-robin"
	p, err := proxy.NewHTTPProxy(cfg.UpstreamTargets, cfg.UpstreamAlgo)
	if err != nil {
		t.Fatal(err)
	}
	s := server.New(cfg, c, p)
	defer s.Shutdown()
	endpoint := httptest.NewServer(s)
	defer endpoint.Close()
	endpoint.Client().Timeout = 15 * time.Second
	type result struct {
		status      int
		declared, n int64
		sha         string
		err         error
	}
	request := func(path, bounds string, slow bool) result {
		req, _ := http.NewRequest("GET", endpoint.URL+path, nil)
		req.Header.Set("Accept-Encoding", "identity")
		req.Host = "bench.local"
		if bounds != "" {
			req.Header.Set("Range", bounds)
		}
		resp, err := endpoint.Client().Do(req)
		if err != nil {
			return result{err: err}
		}
		defer resp.Body.Close()
		h := sha256.New()
		var n int64
		if !slow {
			n, err = io.Copy(h, resp.Body)
		} else {
			first := make([]byte, 1)
			var got int
			got, err = io.ReadFull(resp.Body, first)
			h.Write(first[:got])
			n += int64(got)
			buf := make([]byte, 64<<10)
			for err == nil && n < resp.ContentLength {
				limit := min(int64(len(buf)), resp.ContentLength-n)
				got, err = io.ReadFull(resp.Body, buf[:limit])
				h.Write(buf[:got])
				n += int64(got)
				if got > 0 {
					time.Sleep(time.Millisecond)
				}
			}
		}
		return result{resp.StatusCode, resp.ContentLength, n, fmt.Sprintf("%x", h.Sum(nil)), err}
	}
	for round := 0; round < 12; round++ {
		purge := httptest.NewRequest("PURGE", endpoint.URL+"/large-soak-slow", nil)
		purge.Host = "bench.local"
		purge.Header.Set("Authorization", "Bearer test")
		purged := httptest.NewRecorder()
		s.ServeHTTP(purged, purge)
		if purged.Code != 200 {
			t.Fatalf("purge=%d", purged.Code)
		}
		gate := &slowRangeTrigger{done: make(chan struct{})}
		trigger.Store(gate)
		churnDone := make(chan result, 2)
		go func(round int) {
			select {
			case <-gate.done:
			case <-time.After(10 * time.Second):
				churnDone <- result{err: errors.New("slow source block did not complete")}
				return
			}
			churnDone <- request(fmt.Sprintf("/churn-%d", round), "bytes=0-25165823", false)
		}(round)

		start := make(chan struct{})
		done := make(chan result, 2)
		for worker := 0; worker < 2; worker++ {
			go func(worker int) {
				<-start
				begin := worker * (2 << 20)
				done <- request("/large-soak-slow", fmt.Sprintf("bytes=%d-%d", begin, begin+(4<<20)-1), true)
			}(worker)
		}
		close(start)
		for worker := 0; worker < 2; worker++ {
			got := <-done
			if got.err != nil || got.status != 206 || got.declared != 4<<20 || got.n != 4<<20 || got.sha != "cf83d9e79fa234f1e6b6eaff9084ca8495008df6bbb80c71559d87db7fdb0521" {
				st := s.Stats()
				t.Fatalf("round%d worker%d incomplete: %+v sharedFailed=%d cacheWriteErrors=%d", round, worker, got, st.SharedFillFailed, st.CacheWriteErrors)
			}
		}
		churn := <-churnDone
		if churn.err != nil || churn.status != 206 || churn.n != 24<<20 || churn.sha != "c30d80fa9834d8ea450d31b8ca5e8737add9cfbd548f23a8c4d99f3098c077bb" {
			t.Fatalf("churn prerequisite failed, not target RED: %+v", churn)
		}

	}
}
