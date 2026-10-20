package server

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/Monsterwi/razor/logger"
	"github.com/Monsterwi/razor/proxy"
)

type counters struct{ requests, rejected, cancelled, hits, misses, bypass, bytes, ttfbCount, ttfbNanos, cacheErrors, cacheSkipped atomic.Uint64 }

// Stats contains only aggregate, bounded-cardinality counters. BufferReserved is
// the conservative payload reservation for active writers, not a total RSS cap.
type Stats struct {
	InFlight, Queued, CacheWriters, CacheQueued, DiskQueued                                                                     int
	BufferReserved, DiskBufferedBytes                                                                                           int64
	Requests, Rejected, Cancelled, Hits, Misses, Bypass, Bytes, TTFBCount, TTFBNanoseconds, CacheWriteErrors, CacheWriteSkipped uint64
	Origin                                                                                                                      proxy.Stats
}

func (s *Server) Stats() Stats {
	st := Stats{Requests: s.counters.requests.Load(), Rejected: s.counters.rejected.Load(), Cancelled: s.counters.cancelled.Load(), Hits: s.counters.hits.Load(), Misses: s.counters.misses.Load(), Bypass: s.counters.bypass.Load(), Bytes: s.counters.bytes.Load(), TTFBCount: s.counters.ttfbCount.Load(), TTFBNanoseconds: s.counters.ttfbNanos.Load(), CacheWriteErrors: s.counters.cacheErrors.Load(), CacheWriteSkipped: s.counters.cacheSkipped.Load(), Origin: s.proxy.Stats()}
	if s.admission != nil {
		b := s.admission.Stats()
		st.InFlight = b.InFlight
		st.Queued = b.Queued
	}
	if s.writers != nil {
		b := s.writers.Stats()
		st.CacheWriters = b.InFlight
		st.CacheQueued = b.Queued
		st.BufferReserved = int64(b.InFlight) * int64(s.config.CacheWriterReservation())
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
