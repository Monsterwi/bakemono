package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/Monsterwi/razor/logger"
)

// Stripe represents a cache volume/stripe.
type Stripe struct {
	DirMgr    *DirManager
	AggBuffer *AggregateWriteBuffer
	OpenDir   *OpenDir
	RamCache  *RamCache
	SM        *StripeSM // State Machine

	Path       string
	Fd         *os.File
	Start      int64 // Start offset of data on disk
	Len        int64 // Length of the stripe
	Skip       int64 // Offset to start of stripe (header)
	SectorSize int

	// WritePos tracks the committed disk offset (start of the next flush).
	WritePos int64
	Phase    bool

	Header *StripeHeader

	mu sync.RWMutex
	// stopChan signals background workers to stop
	stopChan chan struct{}
	wg       sync.WaitGroup

	// Evacuation Management
	EvacMgr *EvacuationTable
	// Hit Evacuate Window (aligned with ATS hit_evacuate_window)
	// Objects within this window from WritePos will be registered for evacuation on read.
	hitEvacuateWindow int64
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

func NewStripe(path string, length int64, skip int64, opts ...StripeOption) *Stripe {
	s := &Stripe{
		DirMgr:     &DirManager{},
		AggBuffer:  NewAggregateWriteBuffer(),
		OpenDir:    NewOpenDir(),
		RamCache:   NewRamCache(1024 * 1024 * 10), // Default 10MB RamCache
		Path:       path,
		Len:        length,
		Skip:       skip,
		SectorSize: SectorSize,
		stopChan:   make(chan struct{}),
		EvacMgr:    NewEvacuationTable(),
	}

	for _, opt := range opts {
		opt(s)
	}

	// Initialize State Machine
	s.SM = NewStripeSM(s)

	return s
}

// Init initializes the stripe.
func (s *Stripe) Init(blocks int64) error {
	// Initialize DirManager
	avgObjSize := int64(8192)
	dirNum := s.Len / avgObjSize
	s.DirMgr.Init(Offset(dirNum))

	// Calculate Metadata Size
	metaSize := int64(StripeHeaderSize + s.DirMgr.BinarySize())
	metaSize = (metaSize + int64(SectorSize) - 1) / int64(SectorSize) * int64(SectorSize)

	s.Start = s.Skip + metaSize
	s.WritePos = s.Start

	// Initialize AggBuffer cursor
	s.AggBuffer.Init(s.Start)
	// Set bounds: [Start, Skip+Len)
	s.AggBuffer.SetBounds(s.Start, s.Skip+s.Len)

	// Initialize hit evacuate window (aligned with ATS)
	s.recomputeHitEvacuateWindow()

	return nil
}

func (s *Stripe) Open() error {
	// Ensure the directory exists before opening the file
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	f, err := os.OpenFile(s.Path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	s.Fd = f

	// Attempt to load header and directory
	if err := s.Load(); err != nil {
		logger.Infof("Stripe Load failed (new file or corrupt), initializing new: %v", err)
		if err := s.Save(); err != nil {
			logger.Errorf("Failed to save initial stripe state: %v", err)
		} else {
			logger.Debugf("Saved initial stripe state for %s", s.Path)
		}
	} else {
		logger.Infof("Successfully loaded stripe state from %s: WritePos=%d, Phase=%v", s.Path, s.WritePos, s.Phase)
		// Update AggBuffer cursor to match loaded state
		s.AggBuffer.Init(s.WritePos)
	}

	// Start the SM background task
	s.SM.Start()

	// Start background flusher
	s.StartBackgroundFlusher()

	return nil
}

func (s *Stripe) Close() error {
	if s.SM != nil {
		s.SM.Stop()
	}

	if s.Fd != nil {
		// Flush any pending data in AggBuffer before saving metadata
		if err := s.FlushAggBufferSync(); err != nil {
			logger.Errorf("Failed to flush AggBuffer during Close: %v", err)
		}

		// Stop background workers
		close(s.stopChan)
		s.wg.Wait()

		// Save state (Header + Directory) before closing
		if err := s.Save(); err != nil {
			logger.Errorf("Failed to save stripe state during Close: %v", err)
		} else {
			logger.Debugf("Saved stripe state before close: WritePos=%d, Phase=%v", s.WritePos, s.Phase)
		}

		if err := s.Fd.Close(); err != nil {
			logger.Errorf("Failed to close stripe file: %v", err)
			return err
		}
		s.Fd = nil
	}
	return nil
}

// Save persists the Stripe state (Header + Directory) to disk.
func (s *Stripe) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Fd == nil {
		return fmt.Errorf("stripe not open")
	}

	header := StripeHeader{
		Magic:    StripeHeaderMagic,
		Version:  StripeHeaderVer,
		WritePos: s.WritePos,
		Phase:    s.Phase,
	}

	headerBytes, err := header.MarshalBinary()
	if err != nil {
		return err
	}

	dirBytes, err := s.DirMgr.MarshalBinary()
	if err != nil {
		return err
	}

	if _, err := s.Fd.WriteAt(headerBytes, s.Skip); err != nil {
		return err
	}

	dirOffset := s.Skip + int64(len(headerBytes))
	if _, err := s.Fd.WriteAt(dirBytes, dirOffset); err != nil {
		logger.Errorf("Failed to write directory at offset %d: %v", dirOffset, err)
		return fmt.Errorf("failed to write directory: %w", err)
	}

	if err := s.Fd.Sync(); err != nil {
		logger.Errorf("Failed to sync stripe file: %v", err)
		return fmt.Errorf("failed to sync: %w", err)
	}

	logger.Debugf("Saved stripe metadata: WritePos=%d, Phase=%v, DirSize=%d", s.WritePos, s.Phase, len(dirBytes))
	return nil
}

// Load restores the Stripe state from disk.
func (s *Stripe) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Fd == nil {
		return fmt.Errorf("stripe not open")
	}

	headerBytes := make([]byte, StripeHeaderSize)
	if _, err := s.Fd.ReadAt(headerBytes, s.Skip); err != nil {
		return err
	}

	var header StripeHeader
	if err := header.UnmarshalBinary(headerBytes); err != nil {
		return err
	}

	if header.Magic != StripeHeaderMagic {
		return fmt.Errorf("invalid stripe magic: %x", header.Magic)
	}

	dirSize := s.DirMgr.BinarySize()
	dirBytes := make([]byte, dirSize)

	dirOffset := s.Skip + int64(len(headerBytes))
	if _, err := s.Fd.ReadAt(dirBytes, dirOffset); err != nil {
		logger.Errorf("Failed to read directory from offset %d: %v", dirOffset, err)
		return fmt.Errorf("failed to read directory: %w", err)
	}

	if err := s.DirMgr.UnmarshalBinary(dirBytes); err != nil {
		logger.Errorf("Failed to unmarshal directory: %v", err)
		return fmt.Errorf("failed to unmarshal directory: %w", err)
	}

	logger.Debugf("Loaded directory: SegmentsNum=%d, BucketsNumPerSegment=%d", s.DirMgr.SegmentsNum, s.DirMgr.BucketsNumPerSegment)

	s.WritePos = header.WritePos
	s.Phase = header.Phase
	s.Header = &header

	return nil
}

// StartBackgroundFlusher starts the disk writer goroutine
func (s *Stripe) StartBackgroundFlusher() {
	s.wg.Add(1)
	go s.diskWriter()
}

// diskWriter consumes tasks from AggBuffer and writes to disk
func (s *Stripe) diskWriter() {
	defer s.wg.Done()

	flushChan := s.AggBuffer.GetFlushChan()

	for {
		select {
		case task, ok := <-flushChan:
			if !ok {
				return
			}
			s.handleFlushTask(task)
		case <-s.stopChan:
			return
		}
	}
}

// handleFlushTask processes a single flush task
func (s *Stripe) handleFlushTask(task FlushTask) {
	bytesPending := len(task.Buffer)
	if bytesPending == 0 {
		s.AggBuffer.NotifyFlushDone()
		return
	}

	s.mu.RLock()
	writePos := s.WritePos
	start := s.Start
	length := s.Len
	phase := s.Phase
	s.mu.RUnlock()

	// 1. Evacuation Logic
	if writePos+int64(bytesPending) > start+length {
		// Wrap around
		// We discard the tail write and wrap to Start.
		// Note: We are losing the bytes that don't fit?
		// Bakemono's original implementation implied wrap-around logic just moves WritePos.
		// But if we have data to write, where does it go?
		// If we are at End, we should ideally split.
		// But assuming we just wrap-and-overwrite from Start.
		// The data in `task.Buffer` is written at `Start`.

		writePos = start
		phase = !phase

		victims, victimsInfo, err := s.collectEvacuationData(start, start+int64(bytesPending))
		if err != nil {
			logger.Errorf("Evacuation failed: %v", err)
		}
		s.requeueVictims(victims, victimsInfo, phase)
	} else {
		// Normal evacuation
		victims, victimsInfo, err := s.collectEvacuationData(writePos, writePos+int64(bytesPending))
		if err != nil {
			logger.Errorf("Evacuation failed: %v", err)
		}
		s.requeueVictims(victims, victimsInfo, phase)
	}

	// 2. Write to Disk
	if _, err := s.Fd.WriteAt(task.Buffer, writePos); err != nil {
		logger.Errorf("Flush write failed: %v", err)
		for _, w := range task.Writers {
			if w != nil {
				w.OnLogFlush(err)
			}
		}
	} else {
		for _, w := range task.Writers {
			if w != nil {
				w.OnLogFlush(nil)
			}
		}
	}

	// 3. Update State
	s.mu.Lock()
	s.WritePos = writePos + int64(bytesPending)
	logger.Debugf("Flush Done. New WritePos: %d", s.WritePos)
	s.Phase = phase
	s.mu.Unlock()

	// 4. Notify Buffer available
	s.AggBuffer.NotifyFlushDone()
}

func (s *Stripe) requeueVictims(victims [][]byte, victimsInfo []struct {
	Key  []byte
	Size int
}, phase bool) {
	for i, data := range victims {
		info := victimsInfo[i]
		approxSize := info.Size

		go func(d []byte, sz int, k []byte, ph bool) {
			// 1. Add to AggBuffer
			newOffset, err := s.AggBuffer.Add(d, sz, nil)
			if err != nil {
				logger.Errorf("Evacuation add failed: %v", err)
				return
			}

			// 2. Populate RamCache
			if s.RamCache != nil {
				s.RamCache.Put(k, d)
			}

			// 3. Update Directory with the correct phase
			if s.DirMgr != nil {
				_, err := s.DirMgr.Set(k, Offset(newOffset), sz, ph)
				if err != nil {
					logger.Errorf("Evacuation dir update failed: %v", err)
				}
			}
		}(data, approxSize, info.Key, phase)
	}
}

// FlushAggBuffer triggers an async flush.
func (s *Stripe) FlushAggBuffer() error {
	s.AggBuffer.TriggerFlush()
	return nil
}

// FlushAggBufferSync flushes the aggregate write buffer to disk synchronously.
func (s *Stripe) FlushAggBufferSync() error {
	return s.AggBuffer.FlushSync()
}

// collectEvacuationData scans the directory for valid entries in the given range and reads them.
func (s *Stripe) collectEvacuationData(start, end int64) ([][]byte, []struct {
	Key  []byte
	Size int
}, error) {
	// Optimization: Use EvacuationTable instead of scanning DirMgr
	candidates := s.EvacMgr.GetCandidatesInRange(start, end)

	var data [][]byte
	var info []struct {
		Key  []byte
		Size int
	}

	for _, entry := range candidates {
		dirOff := entry.Offset

		// 1. Read doc header
		headerBuf := make([]byte, DocHeaderSize)
		_, err := s.Fd.ReadAt(headerBuf, dirOff)
		if err != nil {
			continue
		}
		var doc Doc
		if err := doc.UnmarshalBinary(headerBuf); err != nil {
			continue
		}

		if doc.Magic != DocMagic {
			continue
		}

		// Verify Key matches to ensure we are evacuating the right object
		if doc.VMajor != docIdentityVersion || doc.Key != entry.Key {
			continue
		}

		// 2. Read entire document
		// Use doc.Len/TotalLen from disk or trusted entry.Size?
		// Disk is authoritative.
		docSize := int64(DocHeaderSize) + int64(doc.HLen) + int64(doc.TotalLen)

		// Align docSize
		approxSize := int((docSize + int64(s.SectorSize) - 1) / int64(s.SectorSize) * int64(s.SectorSize))

		docData := make([]byte, docSize)
		_, err = s.Fd.ReadAt(docData, dirOff)
		if err != nil {
			continue
		}

		data = append(data, docData)

		keyCopy := append([]byte(nil), entry.LogicalKey...)

		info = append(info, struct {
			Key  []byte
			Size int
		}{Key: keyCopy, Size: approxSize})

		logger.Debugf("Evacuating doc at %d, size %d", dirOff, docSize)
	}

	return data, info, nil
}

// recomputeHitEvacuateWindow recalculates the hit evacuate window.
// Aligned with ATS StripeSM::recompute_hit_evacuate_window().
func (s *Stripe) recomputeHitEvacuateWindow() {
	// Calculate data blocks (excluding metadata)
	dataBlocks := (s.Len - (s.Start - s.Skip)) / int64(SectorSize)
	// Use 10% of data blocks as window (aligned with ATS cache_config_hit_evacuate_percent)
	s.hitEvacuateWindow = (dataBlocks * 10) / 100
	// Ensure minimum window size
	if s.hitEvacuateWindow < (10*1024*1024)/int64(SectorSize) {
		s.hitEvacuateWindow = (10 * 1024 * 1024) / int64(SectorSize)
	}
	// Convert back to bytes
	s.hitEvacuateWindow = s.hitEvacuateWindow * int64(SectorSize)
}

// withinHitEvacuateWindow checks if a directory entry is within the hit evacuate window.
// Aligned with ATS StripeSM::within_hit_evacuate_window().
func (s *Stripe) withinHitEvacuateWindow(dirOffset int64) bool {
	s.mu.RLock()
	writePos := s.WritePos
	length := s.Len
	start := s.Start
	s.mu.RUnlock()

	var distance int64
	if dirOffset >= writePos {
		distance = dirOffset - writePos
	} else {
		// Wrapped
		distance = (start + length - writePos) + (dirOffset - start)
	}

	return distance < s.hitEvacuateWindow
}

// DirAggBufValid checks if a directory entry is within the aggregation buffer.
// Aligned with ATS Stripe::dir_agg_buf_valid.
func (s *Stripe) DirAggBufValid(d *Dir) bool {
	// Check phase first - if phases don't match, this is a stale entry
	s.mu.RLock()
	phase := s.Phase
	s.mu.RUnlock()

	if phase != d.phase() {
		return false
	}

	return s.AggBuffer.DirAggBufValid(int64(d.offset()))
}
