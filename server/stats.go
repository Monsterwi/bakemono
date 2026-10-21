package server

import (
	"net/http"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/Monsterwi/razor/logger"
	"github.com/Monsterwi/razor/proxy"
)

type counters struct {
	rangeHits, rangeMisses, rangeHitBytes, rangeMissBytes                                                       atomic.Uint64
	requests, rejected, cancelled, hits, misses, bypass, bytes, ttfbCount, ttfbNanos, cacheErrors, cacheSkipped atomic.Uint64
}

// Stats contains only aggregate, bounded-cardinality counters. BufferReserved is
// the conservative payload reservation for writers and block readers, not a total RSS cap.
type Stats struct {
	Goroutines                                                                                                                  int
	RuntimeVersion                                                                                                              uint64
	RetiredRuntimes                                                                                                             int
	InFlight, Queued, CacheWriters, CacheQueued, RangeReaders, RangeReaderQueued, DiskQueued                                    int
	BufferReserved, DiskBufferedBytes                                                                                           int64
	Requests, Rejected, Cancelled, Hits, Misses, Bypass, Bytes, TTFBCount, TTFBNanoseconds, CacheWriteErrors, CacheWriteSkipped uint64
	SharedFillTasks, SharedFillWaiters                                                                                          int
	SharedFillJoined, SharedFillCompleted, SharedFillUnshareable, SharedFillFailed, SharedFillCancelled                         uint64
	RangeBlockHits, RangeBlockMisses, RangeHitBytes, RangeMissBytes                                                             uint64
	Origin                                                                                                                      proxy.Stats
}

func (s *Server) Stats() Stats {
	st := Stats{Goroutines: runtime.NumGoroutine(), Requests: s.counters.requests.Load(), Rejected: s.counters.rejected.Load(), Cancelled: s.counters.cancelled.Load(), Hits: s.counters.hits.Load(), Misses: s.counters.misses.Load(), Bypass: s.counters.bypass.Load(), Bytes: s.counters.bytes.Load(), TTFBCount: s.counters.ttfbCount.Load(), TTFBNanoseconds: s.counters.ttfbNanos.Load(), CacheWriteErrors: s.counters.cacheErrors.Load(), CacheWriteSkipped: s.counters.cacheSkipped.Load()}
	st.RangeBlockHits, st.RangeBlockMisses = s.counters.rangeHits.Load(), s.counters.rangeMisses.Load()
	st.RangeHitBytes, st.RangeMissBytes = s.counters.rangeHitBytes.Load(), s.counters.rangeMissBytes.Load()
	s.mu.Lock()
	st.Origin = s.closedOrigin
	if s.current != nil {
		st.RuntimeVersion = s.current.version
	}
	if s.retired != nil {
		st.RetiredRuntimes = 1
	}
	for _, rt := range []*runtimeSnapshot{s.current, s.retired} {
		if rt == nil {
			continue
		}
		b := rt.admission.Stats()
		st.InFlight += b.InFlight
		st.Queued += b.Queued
		b = rt.writers.Stats()
		st.CacheWriters += b.InFlight
		st.CacheQueued += b.Queued
		st.BufferReserved += int64(b.InFlight) * int64(rt.config.CacheWriterReservation()-rt.config.RangeReaderReservation())
		b = rt.rangeReaders.Stats()
		st.RangeReaders += b.InFlight
		st.RangeReaderQueued += b.Queued
		st.BufferReserved += int64(b.InFlight) * int64(rt.config.RangeReaderReservation())
		addOriginStats(&st.Origin, rt.proxy.Stats())
	}
	s.mu.Unlock()

	if s.fills != nil {
		s.fills.mu.Lock()
		st.SharedFillTasks, st.SharedFillWaiters = s.fills.active, s.fills.waiters
		st.SharedFillJoined, st.SharedFillCompleted, st.SharedFillUnshareable = s.fills.joined, s.fills.completed, s.fills.unshareable
		st.SharedFillFailed, st.SharedFillCancelled = s.fills.failed, s.fills.cancelled
		s.fills.mu.Unlock()
	}
	if s.cache != nil {
		for _, stripe := range s.cache.Stripes {
			q, b := stripe.AggBuffer.Stats()
			st.DiskQueued += q
			st.DiskBufferedBytes += b
		}
	}
	return st
}
func (s *Server) cacheStatus(w http.ResponseWriter, status string) {
	logger.SetCacheStatus(w, status)
	switch status {
	case "HIT":
		s.counters.hits.Add(1)
	case "MISS":
		s.counters.misses.Add(1)
	case "BYPASS":
		s.counters.bypass.Add(1)
	}
}

type responseMetrics struct {
	http.ResponseWriter
	stats   *counters
	start   time.Time
	started bool
}

func (w *responseMetrics) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseMetrics) WriteHeader(status int) {
	if status >= 200 && !w.started {
		w.started = true
		w.stats.ttfbCount.Add(1)
		w.stats.ttfbNanos.Add(uint64(time.Since(w.start)))
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseMetrics) Write(p []byte) (int, error) {
	if !w.started {
		w.WriteHeader(200)
	}
	n, err := w.ResponseWriter.Write(p)
	w.stats.bytes.Add(uint64(n))
	return n, err
}
func (w *responseMetrics) FlushError() error {
	if !w.started {
		w.WriteHeader(200)
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func addOriginStats(a *proxy.Stats, b proxy.Stats) {
	a.InFlight += b.InFlight
	a.Queued += b.Queued
	a.Rejected += b.Rejected
	a.Requests += b.Requests
	a.Errors += b.Errors
	a.Bytes += b.Bytes
	a.TTFBCount += b.TTFBCount
	a.TTFBNanoseconds += b.TTFBNanoseconds
}
