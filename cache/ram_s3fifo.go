// Licensed to the Apache Software Foundation (ASF) under one or more contributor
// license agreements. See THIRD_PARTY_NOTICES.md. Licensed under Apache-2.0;
// https://www.apache.org/licenses/LICENSE-2.0 . Provided AS IS, without warranties.
//
// Go adaptation of ATS 10.2.0 RamCacheS3FIFO.cc at b1df7334b39a878971c376da849e8da4984918b6.
// The adaptation charges payload separately from bounded metadata; see docs/ram_cache.md.
package cache

import (
	"container/list"
	"sync"

	"github.com/maypok86/otter/v2/stats"
)

const (
	fifoSmall = iota
	fifoMain
	fifoGhost
)

type fifoEntry struct {
	key     string
	value   *ramEntry
	charge  uint64
	freq    uint8
	segment int
	element *list.Element
}

type s3FIFO struct {
	mu                                                                       sync.Mutex
	capacity, smallBytes, mainBytes, payloadBytes, ghostBytes, ghostMetadata uint64
	entryLimit                                                               int
	entries                                                                  map[string]*fifoEntry
	queues                                                                   [3]list.List
	counters                                                                 stats.Stats
	promotions, ghostHits                                                    uint64
}

func newS3FIFO(capacity uint64) *s3FIFO {
	return &s3FIFO{capacity: capacity, entryLimit: int(min(capacity, 65536)), entries: make(map[string]*fifoEntry)}
}
func (c *s3FIFO) get(key string) (*ramEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[key]; e != nil && e.segment != fifoGhost {
		e.freq = min(3, e.freq+1)
		c.counters.Hits++
		return e.value, true
	}
	c.counters.Misses++
	return nil, false
}
func (c *s3FIFO) unlink(e *fifoEntry) {
	c.queues[e.segment].Remove(e.element)
	switch e.segment {
	case fifoSmall:
		c.smallBytes -= e.charge
		c.payloadBytes -= uint64(len(e.value.data))
	case fifoMain:
		c.mainBytes -= e.charge
		c.payloadBytes -= uint64(len(e.value.data))
	case fifoGhost:
		c.ghostBytes -= e.charge
		c.ghostMetadata -= uint64(128 + len(e.key))
	}
	delete(c.entries, e.key)
}
func (c *s3FIFO) append(e *fifoEntry, segment int) {
	e.segment = segment
	e.element = c.queues[segment].PushBack(e)
	c.entries[e.key] = e
	switch segment {
	case fifoSmall:
		c.smallBytes += e.charge
		c.payloadBytes += uint64(len(e.value.data))
	case fifoMain:
		c.mainBytes += e.charge
		c.payloadBytes += uint64(len(e.value.data))
	case fifoGhost:
		c.ghostBytes += e.charge
		c.ghostMetadata += uint64(128 + len(e.key))
	}
}
func (c *s3FIFO) trimGhost() {
	for c.ghostBytes > c.capacity || c.ghostMetadata > max(128, c.capacity/10) || c.queues[fifoGhost].Len() > c.entryLimit {
		c.unlink(c.queues[fifoGhost].Front().Value.(*fifoEntry))
	}
}
func (c *s3FIFO) evict() {
	// ATS variant: choose main only above its 90% target or with an empty small queue.
	if c.mainBytes > c.capacity*9/10 || c.queues[fifoSmall].Len() == 0 {
		for c.queues[fifoMain].Len() > 0 {
			e := c.queues[fifoMain].Front().Value.(*fifoEntry)
			if e.freq > 0 {
				e.freq--
				c.queues[fifoMain].MoveToBack(e.element)
				continue
			}
			c.counters.Evictions++
			c.counters.EvictionWeight += e.charge
			c.unlink(e)
			return
		}
	}
	for c.queues[fifoSmall].Len() > 0 {
		e := c.queues[fifoSmall].Front().Value.(*fifoEntry)
		c.unlink(e)
		if e.freq >= 2 {
			e.freq = 0
			c.append(e, fifoMain)
			c.promotions++
			continue
		}
		c.counters.Evictions++
		c.counters.EvictionWeight += e.charge
		e.value = nil
		e.freq = 0
		c.append(e, fifoGhost)
		c.trimGhost()
		return
	}
}
func (c *s3FIFO) put(key string, value *ramEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	segment := fifoSmall
	charge := uint64(value.weight)
	if old := c.entries[key]; old != nil {
		if old.segment != fifoGhost {
			c.payloadBytes = c.payloadBytes - uint64(len(old.value.data)) + uint64(len(value.data))
			if old.segment == fifoSmall {
				c.smallBytes = c.smallBytes - old.charge + charge
			} else {
				c.mainBytes = c.mainBytes - old.charge + charge
			}
			old.value, old.charge, old.freq = value, charge, min(3, old.freq+1)
			for c.smallBytes+c.mainBytes > c.capacity {
				c.evict()
			}
			return
		}
		segment = fifoMain
		c.ghostHits++
		c.unlink(old)
	}
	for c.smallBytes+c.mainBytes+charge > c.capacity || c.queues[fifoSmall].Len()+c.queues[fifoMain].Len() >= c.entryLimit {
		c.evict()
	}
	c.append(&fifoEntry{key: key, value: value, charge: charge}, segment)
}
func (c *s3FIFO) delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[key]; e != nil {
		c.unlink(e)
	}
}
func (c *s3FIFO) snapshot() RAMStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := RAMStats{Stats: c.counters, ResidentBytes: c.payloadBytes, ChargedBytes: c.smallBytes + c.mainBytes, ResidentEntries: c.queues[fifoSmall].Len() + c.queues[fifoMain].Len(), SmallEntries: c.queues[fifoSmall].Len(), MainEntries: c.queues[fifoMain].Len(), GhostEntries: c.queues[fifoGhost].Len(), GhostBytes: c.ghostBytes, Promotions: c.promotions, GhostHits: c.ghostHits, EstimatedMetadataBytes: c.ghostMetadata}
	for _, e := range c.entries {
		if e.segment != fifoGhost {
			decoded := e.charge - uint64(max(1, len(e.value.data)))
			st.DecodedMetadataBytes += decoded
			st.EstimatedMetadataBytes += uint64(128+len(e.key)) + decoded
		}
	}
	return st
}
