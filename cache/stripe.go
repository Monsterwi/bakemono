package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// stripeFile is the filesystem boundary used for deterministic I/O failures.
type stripeFile interface {
	ReadAt([]byte, int64) (int, error)
	WriteAt([]byte, int64) (int, error)
	Sync() error
	Close() error
}

type Stripe struct {
	DirMgr                   *DirManager
	AggBuffer                *AggregateWriteBuffer
	OpenDir                  *OpenDir
	RamCache                 *RamCache
	SM                       *StripeSM
	Path                     string
	Fd                       *os.File
	disk                     stripeFile
	Start, Len, Skip         int64
	SectorSize               int
	WritePos                 int64
	Phase                    bool
	Header                   *StripeHeader
	ioMu                     sync.Mutex
	writingStart, writingEnd int64
	writingGeneration        uint64
	mu                       sync.RWMutex
	generation               uint64
	storageErr               error
	closing                  bool
	closeOnce                sync.Once
	closeErr                 error
	stopChan                 chan struct{}
	wg                       sync.WaitGroup
	EvacMgr                  *EvacuationTable
	hitEvacuateWindow        int64
	evacCtx                  context.Context
	evacCancel               context.CancelFunc
	evacQueue                chan []evacuationRecord
}

type evacuationRecord struct {
	key, data  []byte
	old        Dir
	generation uint64
}
type StripeOption func(*Stripe)

func WithRamCache(size uint64) StripeOption {
	return func(s *Stripe) {
		if size > 0 {
			s.RamCache = NewRamCache(size)
		} else {
			s.RamCache = nil
		}
	}
}
func NewStripe(path string, length, skip int64, opts ...StripeOption) *Stripe {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Stripe{DirMgr: &DirManager{}, AggBuffer: NewAggregateWriteBuffer(), OpenDir: NewOpenDir(), RamCache: NewRamCache(10 << 20), Path: path, Len: length, Skip: skip, SectorSize: SectorSize, stopChan: make(chan struct{}), EvacMgr: NewEvacuationTable(), evacCtx: ctx, evacCancel: cancel, evacQueue: make(chan []evacuationRecord, 1)}
	for _, opt := range opts {
		opt(s)
	}
	s.SM = NewStripeSM(s)
	return s
}
func (s *Stripe) Init(_ int64) error {
	if s.Skip < 0 || s.Len < 32768 || s.Skip+s.Len < s.Skip {
		return fmt.Errorf("invalid stripe bounds")
	}
	s.DirMgr.Init(Offset(s.Len / 8192))
	meta := int64(StripeHeaderSize + s.DirMgr.BinarySize())
	meta = (meta + SectorSize - 1) / SectorSize * SectorSize
	s.Start = s.Skip + meta
	if s.Start >= s.Skip+s.Len {
		return fmt.Errorf("stripe has no data area")
	}
	s.WritePos = s.Start
	s.AggBuffer.Init(s.Start)
	s.AggBuffer.SetBounds(s.Start, s.Skip+s.Len)
	s.recomputeHitEvacuateWindow()
	return nil
}
func (s *Stripe) Open() error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(s.Path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	s.Fd = f
	s.disk = f
	if err = s.Load(); err != nil {
		if err = s.Save(); err != nil {
			_ = f.Close()
			s.Fd = nil
			return err
		}
	}
	s.AggBuffer.initPosition(s.WritePos, s.Phase)
	if s.Phase {
		s.generation = 1
	}
	s.StartBackgroundFlusher()
	s.wg.Add(1)
	go s.evacuate()
	s.SM.Start()
	return nil
}
func (s *Stripe) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		s.AggBuffer.stopAdds()
		s.evacCancel()
		if s.SM != nil {
			s.SM.Stop()
		}
		flushErr := s.FlushAggBufferSync()
		close(s.stopChan)
		s.wg.Wait()
		s.ioMu.Lock()
		defer s.ioMu.Unlock()
		saveErr := s.saveSnapshot()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.disk == nil {
			s.closeErr = flushErr
			return
		}
		closeErr := s.disk.Close()
		s.Fd = nil
		s.disk = nil
		s.closeErr = errors.Join(flushErr, saveErr, closeErr)
	})
	return s.closeErr
}

// Save acknowledges queued bytes before persisting a metadata snapshot. This
// in-place checkpoint is not crash-atomic; successful WriteAt alone is not fsync.
func (s *Stripe) Save() error {
	if err := s.FlushAggBufferSync(); err != nil {
		return err
	}
	s.ioMu.Lock()
	defer s.ioMu.Unlock()
	return s.saveSnapshot()
}
func writeFullAt(f stripeFile, p []byte, off int64) error {
	n, err := f.WriteAt(p, off)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return err
}
func (s *Stripe) failLocked(err error) error {
	if err != nil && s.storageErr == nil {
		s.storageErr = err
		s.AggBuffer.Fail(err)
	}
	return s.storageErr
}

// Caller holds ioMu; snapshot publication is atomic, disk I/O never holds mu.
func (s *Stripe) saveSnapshot() error {
	s.mu.Lock()
	if s.storageErr != nil {
		err := s.storageErr
		s.mu.Unlock()
		return err
	}
	if s.disk == nil {
		s.mu.Unlock()
		return ErrCacheClosed
	}
	h := StripeHeader{Magic: StripeHeaderMagic, Version: StripeHeaderVer, WritePos: s.WritePos, Phase: s.Phase}
	header, err := h.MarshalBinary()
	if err != nil {
		s.mu.Unlock()
		return err
	}
	dirs, err := s.DirMgr.MarshalBinary()
	f := s.disk
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err = writeFullAt(f, header, s.Skip); err == nil {
		err = writeFullAt(f, dirs, s.Skip+int64(len(header)))
	}
	if err == nil {
		err = f.Sync()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failLocked(err)
}

func (s *Stripe) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disk == nil {
		return ErrCacheClosed
	}
	buf := make([]byte, StripeHeaderSize)
	if _, err := s.disk.ReadAt(buf, s.Skip); err != nil {
		return err
	}
	var h StripeHeader
	if err := h.UnmarshalBinary(buf); err != nil {
		return err
	}
	if h.Magic != StripeHeaderMagic || h.Version != StripeHeaderVer || h.WritePos < s.Start || h.WritePos > s.Skip+s.Len {
		return fmt.Errorf("invalid stripe header")
	}
	dirs := make([]byte, s.DirMgr.BinarySize())
	if _, err := s.disk.ReadAt(dirs, s.Skip+int64(len(buf))); err != nil {
		return err
	}
	if err := s.DirMgr.UnmarshalBinary(dirs); err != nil {
		return err
	}
	s.WritePos, s.Phase, s.Header = h.WritePos, h.Phase, &h
	return nil
}
func (s *Stripe) StartBackgroundFlusher() { s.wg.Add(1); go s.diskWriter() }
func (s *Stripe) diskWriter() {
	defer s.wg.Done()
	for {
		select {
		case task := <-s.AggBuffer.GetFlushChan():
			s.handleFlushTask(task)
		case <-s.stopChan:
			return
		}
	}
}
func (s *Stripe) handleFlushTask(task FlushTask) {
	s.ioMu.Lock()
	s.mu.Lock()
	err := s.storageErr
	var victims []evacuationRecord
	end := task.Offset + int64(len(task.Buffer))
	if err == nil && (task.Offset < s.Start || end > s.Skip+s.Len) {
		err = fmt.Errorf("flush outside stripe data area")
	}
	if err == nil {
		victims = s.collectEvacuationData(task.Offset, end)
		// A phase bit can alias after two wraps. Remove its old entries before reuse.
		if task.Generation != s.generation {
			s.DirMgr.removePhase(task.Generation%2 != 0)
		}
		s.writingStart, s.writingEnd = task.Offset, end
		s.writingGeneration = task.Generation
		s.mu.Unlock()
		err = writeFullAt(s.disk, task.Buffer, task.Offset)
		s.mu.Lock()
		s.writingStart, s.writingEnd = 0, 0
	}
	if err == nil {
		s.WritePos = end
		s.generation = task.Generation
		s.Phase = task.Generation%2 != 0
	} else {
		s.failLocked(err)
	}
	s.mu.Unlock()
	s.ioMu.Unlock()
	for _, w := range task.Writers {
		if w != nil {
			w.OnLogFlush(err)
		}
	}
	s.AggBuffer.NotifyFlushDone(err)
	if err == nil && len(victims) > 0 {
		select {
		case s.evacQueue <- victims:
		case <-s.evacCtx.Done():
		default:
			// shortcut: a full bounded evacuation queue evicts cold/overwritten entries;
			// tune its budget only with measured hot-object retention requirements.
		}
	}
}
func (s *Stripe) FlushAggBuffer() error     { s.AggBuffer.TriggerFlush(); return s.AggBuffer.Err() }
func (s *Stripe) FlushAggBufferSync() error { return s.AggBuffer.FlushSync() }
func (s *Stripe) writeError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.storageErr != nil {
		return s.storageErr
	}
	if s.closing || s.disk == nil {
		return ErrCacheClosed
	}
	return nil
}
func (s *Stripe) allocationValid(a allocation, size int) bool {
	if s.writingEnd > 0 && a.generation != s.writingGeneration && a.offset < s.writingEnd && a.offset+int64(size) > s.writingStart {
		return false
	}
	if a.offset < s.Start || a.offset+int64(size) > s.Skip+s.Len {
		return false
	}
	return (a.generation == s.generation && a.offset+int64(size) <= s.WritePos) || (a.generation+1 == s.generation && a.offset >= s.WritePos)
}
func (s *Stripe) dirValid(d Dir) bool {
	off := int64(d.offset())
	if s.writingEnd > 0 && d.phase() != (s.writingGeneration%2 != 0) && off < s.writingEnd && off+int64(d.approxSize()) > s.writingStart {
		return false
	}
	if s.storageErr != nil || s.disk == nil || off < s.Start || off >= s.Skip+s.Len {
		return false
	}
	if d.phase() == s.Phase {
		return off < s.WritePos
	}
	return off >= s.WritePos
}

func (s *Stripe) collectEvacuationData(start, end int64) []evacuationRecord {
	var result []evacuationRecord
	bytes := 0
	for _, e := range s.EvacMgr.GetCandidatesInRange(start, end) {
		hit, _, d := s.DirMgr.Get(e.LogicalKey)
		if !hit || int64(d.offset()) != e.Offset || !s.dirValid(d) {
			continue
		}
		header := make([]byte, DocHeaderSize)
		if _, err := s.disk.ReadAt(header, e.Offset); err != nil {
			continue
		}
		var doc Doc
		if doc.UnmarshalBinary(header) != nil || doc.Magic != DocMagic || doc.VMajor != docIdentityVersion || doc.Key != e.Key {
			continue
		}
		size := int(doc.Len)
		if size < DocHeaderSize || size > AggBufferSize || uint64(doc.HLen) > uint64(size-DocHeaderSize) || e.Offset+int64(size) > s.Skip+s.Len || bytes+size > AggBufferSize || int64(size)+end-start > s.Skip+s.Len-s.Start {
			continue
		}
		data := make([]byte, size)
		if _, err := s.disk.ReadAt(data, e.Offset); err != nil {
			continue
		}
		gen := s.generation
		if d.phase() != s.Phase {
			if gen == 0 {
				continue
			}
			gen--
		}
		result = append(result, evacuationRecord{key: e.LogicalKey, data: data, old: d, generation: gen})
		bytes += size
	}
	return result
}
func (s *Stripe) evacuate() {
	defer s.wg.Done()
	for {
		select {
		case <-s.evacCtx.Done():
			return
		case batch := <-s.evacQueue:
			for _, v := range batch {
				a, err := s.AggBuffer.addContext(s.evacCtx, v.data, len(v.data), nil)
				if err != nil {
					break
				}
				if err = s.AggBuffer.flushThrough(s.evacCtx, a.sequence); err != nil {
					break
				}
				s.mu.Lock()
				hit, _, current := s.DirMgr.Get(v.key)
				if !s.closing && s.storageErr == nil && hit && current.offset() == v.old.offset() && current.phase() == v.old.phase() && v.generation+1 >= s.generation && s.allocationValid(a, len(v.data)) {
					_, err = s.DirMgr.Set(v.key, Offset(a.offset), len(v.data), a.generation%2 != 0)
					if err == nil && s.RamCache != nil {
						s.RamCache.Put(v.key, v.data)
					}
				}
				s.mu.Unlock()
			}
		}
	}
}
func (s *Stripe) recomputeHitEvacuateWindow() {
	s.hitEvacuateWindow = (s.Skip + s.Len - s.Start) / 10
	if s.hitEvacuateWindow < 10<<20 {
		s.hitEvacuateWindow = 10 << 20
	}
}
func (s *Stripe) withinHitEvacuateWindow(offset int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	distance := offset - s.WritePos
	if distance < 0 {
		distance += s.Skip + s.Len - s.Start
	}
	return distance < s.hitEvacuateWindow
}
func (s *Stripe) DirAggBufValid(d *Dir) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.dirValid(*d) && s.AggBuffer.DirAggBufValid(int64(d.offset()))
}
