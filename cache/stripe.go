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

	// Write Cursor
	WritePos int64 // Current write position on disk (absolute offset)
	Phase    bool

	Header *StripeHeader

	mu sync.RWMutex
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
		// Start and WritePos will be initialized in Init
	}

	for _, opt := range opts {
		opt(s)
	}

	// Initialize State Machine
	s.SM = NewStripeSM(s)

	return s
}

// Init initializes the stripe.
// blocks is the number of 512-byte blocks.
func (s *Stripe) Init(blocks int64) error {
	// Initialize DirManager
	avgObjSize := int64(8192)
	dirNum := s.Len / avgObjSize
	s.DirMgr.Init(Offset(dirNum))

	// Calculate Metadata Size
	// Header + DirManager Data
	metaSize := int64(StripeHeaderSize + s.DirMgr.BinarySize())

	// Align to SectorSize
	metaSize = (metaSize + int64(SectorSize) - 1) / int64(SectorSize) * int64(SectorSize)

	s.Start = s.Skip + metaSize
	s.WritePos = s.Start

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
		// If load fails (new file or corrupt), we keep the initialized empty state.
		// Log the error for debugging
		logger.Infof("Stripe Load failed (new file or corrupt), initializing new: %v", err)

		// Save the initial empty state so it can be loaded next time
		if err := s.Save(); err != nil {
			logger.Errorf("Failed to save initial stripe state: %v", err)
			// Continue anyway, as this is not critical for operation
		} else {
			logger.Debugf("Saved initial stripe state for %s", s.Path)
		}
	} else {
		logger.Infof("Successfully loaded stripe state from %s: WritePos=%d, Phase=%v", s.Path, s.WritePos, s.Phase)
	}

	// Start the SM background task
	s.SM.Start()

	return nil
}

func (s *Stripe) Close() error {
	// Stop the SM first to flush pending writes
	if s.SM != nil {
		s.SM.Stop()
	}

	if s.Fd != nil {
		// Flush any pending data in AggBuffer before saving metadata
		// This ensures all written data is persisted to disk
		if err := s.FlushAggBuffer(); err != nil {
			// Log error but continue to save metadata
			logger.Errorf("Failed to flush AggBuffer during Close: %v", err)
		}

		// Save state (Header + Directory) before closing
		if err := s.Save(); err != nil {
			logger.Errorf("Failed to save stripe state during Close: %v", err)
			// Continue to close file even if save failed
		} else {
			logger.Debugf("Saved stripe state before close: WritePos=%d, Phase=%v", s.WritePos, s.Phase)
		}

		// Close file and clear reference
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

	// Write Header at Skip
	if _, err := s.Fd.WriteAt(headerBytes, s.Skip); err != nil {
		return err
	}

	// Write Directory immediately after header
	// Note: In Init we assumed Header is small part of metaSize.
	// We should strictly place Dir at known offset.
	// Let's assume Dir starts at Skip + StripeHeaderSize (aligned?)
	// Ideally we align structures.
	// For simplicity, we write sequentially.

	// But we need to be able to read it back.
	// UnmarshalBinary expects just the data.

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

	// Read Header
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

	// Read DirManager
	dirSize := s.DirMgr.BinarySize()
	dirBytes := make([]byte, dirSize)

	// Header marshaled size might vary if variable length?
	// StripeHeader struct is fixed size manually or via binary.Size?
	// We used manual marshal.
	// Let's use the read length.

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

	// Restore state
	s.WritePos = header.WritePos
	s.Phase = header.Phase
	s.Header = &header

	return nil
}

// FlushAggBuffer flushes the aggregate write buffer to disk.
func (s *Stripe) FlushAggBuffer() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.AggBuffer.IsEmpty() {
		return nil
	}

	bytesPending := s.AggBuffer.GetBufferPos()

	// Evacuation Collection
	var victims [][]byte
	var victimsInfo []struct {
		Key  []byte
		Size int
	}

	// Check if we have enough space or need to wrap around
	// Cyclic log logic: if WritePos + bytesPending > Start + Len
	// wrap around to Start.
	if s.WritePos+int64(bytesPending) > s.Start+s.Len {
		// Evacuate region being overwritten
		vData, vInfo, err := s.collectEvacuationData(s.Start, s.Start+int64(bytesPending))
		if err != nil {
			return fmt.Errorf("evacuation failed: %v", err)
		}
		victims = append(victims, vData...)
		victimsInfo = append(victimsInfo, vInfo...)

		s.WritePos = s.Start
		s.Phase = !s.Phase // Flip phase on wrap
	}

	// Also need to evacuate if we are NOT wrapping but overlapping valid data?
	// The cyclic log always overwrites data ahead of WritePos.
	// The region being overwritten is [WritePos, WritePos + bytesPending).
	// We should evacuate this region.

	overwriteEnd := s.WritePos + int64(bytesPending)
	if overwriteEnd > s.Start+s.Len {
		// This case is handled by wrap around above, but let's be precise.
		// If wrapping, we evacuate [Start, Start+remainder]
		// The logic above resets WritePos to Start.
		// So we need to evacuate [Start, Start + bytesPending]
	} else {
		// Normal case: evacuate [WritePos, WritePos + bytesPending]
		vData, vInfo, err := s.collectEvacuationData(s.WritePos, overwriteEnd)
		if err != nil {
			return fmt.Errorf("evacuation failed: %v", err)
		}
		victims = append(victims, vData...)
		victimsInfo = append(victimsInfo, vInfo...)
	}

	// Flush to disk
	err := s.AggBuffer.Flush(s.Fd, s.WritePos)
	if err != nil {
		return fmt.Errorf("flush failed: %v", err)
	}

	// Advance WritePos
	s.WritePos += int64(bytesPending)

	// Reset buffer
	s.AggBuffer.Reset()

	// Re-add victims to buffer
	// They will be flushed in next cycle.
	// This moves them ahead of the write cursor.
	for i, data := range victims {
		info := victimsInfo[i]
		// Note: We don't have the original Key easily from data unless we parse it or store it.
		// We stored Key in victimsInfo.
		// We also need approxSize.
		approxSize := info.Size

		// We need to update directory for these victims?
		// Wait, Add() does NOT update directory. CacheWriter.Close() does.
		// Here we are bypassing CacheWriter.
		// So we must update directory manually for these evacuated items.

		// But we can't update directory until we flush them to disk (know their new address).
		// Since we are just adding to AggBuffer, they are not on disk yet.
		// We need a mechanism to update Dir when AggBuffer flushes?
		// Or we do it optimistically like CacheWriter.Close()?

		// If we add to AggBuffer, we know relative pos.
		currPos := s.AggBuffer.GetBufferPos()
		newDiskOffset := s.WritePos + int64(currPos)

		// Add to buffer
		if err := s.AggBuffer.Add(data, approxSize, nil); err != nil {
			// If buffer full?
			// We just flushed, so it should be empty.
			// Unless victims > buffer size.
			// If victims > buffer, we might need multiple flushes.
			// Recursive flush? Dangerous.
			fmt.Printf("Evacuation add failed: %v\n", err)
			continue
		}

		// Update Directory
		// We use the new offset.
		if s.DirMgr != nil {
			// Update with new location
			// Key is needed.
			s.DirMgr.Set(info.Key, Offset(newDiskOffset), approxSize)
		}
	}

	return nil
}

// collectEvacuationData scans the directory for valid entries in the given range and reads them.
func (s *Stripe) collectEvacuationData(start, end int64) ([][]byte, []struct {
	Key  []byte
	Size int
}, error) {
	var data [][]byte
	var info []struct {
		Key  []byte
		Size int
	}

	// Scan all directories
	// Note: Iterating the entire directory is expensive. In ATS this is optimized.
	for i := 0; i < int(s.DirMgr.SegmentsNum); i++ {
		segID := segId(i)
		s.DirMgr.SegMutexes[segID].RLock()
		dirs := s.DirMgr.Dirs[segID]

		for j := 0; j < len(dirs); j++ {
			d := dirs[j]
			if d.offset() == 0 { // Empty
				continue
			}

			dirOff := int64(d.offset())
			// Simplification: if directory entry falls within the range being overwritten
			if dirOff >= start && dirOff < end {
				if d.pinned() || true { // Evacuate valid
					// We found a victim.
					// Read and move logic:

					// 1. Read doc header to find total size
					headerBuf := make([]byte, DocHeaderSize)
					_, err := s.Fd.ReadAt(headerBuf, dirOff)
					if err != nil {
						// Failed to read, maybe corrupt or race. Skip.
						continue
					}
					var doc Doc
					if err := doc.UnmarshalBinary(headerBuf); err != nil {
						continue
					}

					// 2. Read entire document
					docSize := int64(DocHeaderSize) + int64(doc.HLen) + int64(doc.TotalLen)

					// Align docSize to SectorSize for storage allocation
					approxSize := int((docSize + int64(s.SectorSize) - 1) / int64(s.SectorSize) * int64(s.SectorSize))

					docData := make([]byte, docSize)
					_, err = s.Fd.ReadAt(docData, dirOff)
					if err != nil {
						continue
					}

					data = append(data, docData)

					// Need to copy Key to avoid reference issues if we were using pointers (here Key is value in Doc, but we need to extract it)
					// Doc has Key [16]byte.
					keyCopy := make([]byte, 16)
					copy(keyCopy, doc.Key[:])

					info = append(info, struct {
						Key  []byte
						Size int
					}{Key: keyCopy, Size: approxSize})

					fmt.Printf("Evacuating doc at %d, size %d\n", dirOff, docSize)
				}
			}
		}
		s.DirMgr.SegMutexes[segID].RUnlock()
	}
	return data, info, nil
}
