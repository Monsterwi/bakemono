package bakemono

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"sync"
	"time"
)

const StripeBlockSize uint64 = 1024 * 1024 * 128 // 128MB

var (
	HeaderSize = binary.Size(&StripeHeaderFooter{})
	DirSize    = binary.Size(&Dir{})
	DirIDSize  = binary.Size(uint16(0))
)

type Stripe struct {
	ID int

	Header *StripeHeaderFooter

	SectorSize uint32
	Length     Offset

	// Meta offsets
	HeaderAOffset Offset
	FooterAOffset Offset
	HeaderBOffset Offset
	FooterBOffset Offset

	StartOffset Offset
	DataOffset  Offset // Relative to StartOffset
	WritePos    Offset // Relative to StartOffset

	mutex sync.RWMutex
	Dm    *DirManager

	Fp             *os.File
	aggWriteBuffer *AggregateWriteBuffer
	RamCache       *RamCache
	OpenDir        *OpenDir
}

func NewStripe(id int, fp *os.File, startOffset, length int64, ramCacheSize uint64) *Stripe {
	s := &Stripe{
		ID:             id,
		Fp:             fp,
		StartOffset:    Offset(startOffset),
		Length:         Offset(length),
		aggWriteBuffer: NewAggregateWriteBuffer(fp),
		Dm:             &DirManager{},
		OpenDir:        NewOpenDir(),
	}
	if ramCacheSize > 0 {
		s.RamCache = NewRamCache(ramCacheSize)
	}
	return s
}

// Init initializes the stripe directory and write position.
func (s *Stripe) Init(avgChunkSize uint64) (corrupted bool, err error) {
	expectedDirNum := uint64(s.Length) / avgChunkSize
	s.Dm.Init(Offset(expectedDirNum))

	s.prepareOffsets()

	err = s.buildMetaFromFp()
	if err != nil {
		logger.Warnf("Stripe %d build meta from fp failed, file may corrupted, err: %v", s.ID, err)
		corrupted = true
		s.initEmptyMeta()
	}
	return corrupted, err
}

func (s *Stripe) initEmptyMeta() {
	s.Header = &StripeHeaderFooter{
		Magic:          MagicBocchi,
		CreateUnixTime: time.Now().Unix(),
		WritePos:       s.DataOffset,
		SyncSerial:     0,
		//WriteSerial:    0,
	}
	s.Dm.InitEmptyDirs()
}

// prepareOffsets calculates offsets and block numbers before initing a Stripe.
func (s *Stripe) prepareOffsets() {
	// Calculate metadata size
	dirMetaSize := s.Dm.BinarySize()
	metaSize := 2*HeaderSize + dirMetaSize

	// Align metaSize to 4K (sector size) to ensure data starts at aligned offset?
	// ATS aligns to block size. Let's align to 4096 for good measure.
	if metaSize%4096 != 0 {
		metaSize += 4096 - (metaSize % 4096)
	}

	// Setup Offsets for Meta A and Meta B
	s.HeaderAOffset = s.StartOffset
	s.FooterAOffset = s.StartOffset + Offset(metaSize) - Offset(HeaderSize)
	s.HeaderBOffset = s.StartOffset + Offset(metaSize)
	s.FooterBOffset = s.StartOffset + Offset(metaSize)*2 - Offset(HeaderSize)

	// DataOffset starts after Meta B
	s.DataOffset = Offset(metaSize) * 2
	s.WritePos = s.DataOffset
}

func (s *Stripe) buildMetaFromFp() error {
	// We need to decide which Meta to use (A or B)
	// 1. Try read Meta A
	headerA, _, errA := s.readMeta(s.HeaderAOffset, s.FooterAOffset)
	validA := errA == nil

	// 2. Try read Meta B
	headerB, _, errB := s.readMeta(s.HeaderBOffset, s.FooterBOffset)
	validB := errB == nil

	// 3. Decide
	var header *StripeHeaderFooter
	var headerOffset Offset

	if !validA && !validB {
		return errors.New("both meta A and B are invalid")
	} else if validA && !validB {
		header = headerA
		headerOffset = s.HeaderAOffset
		logger.Infof("Stripe %d: Meta A valid, Meta B invalid. Using A.", s.ID)
	} else if !validA && validB {
		header = headerB
		headerOffset = s.HeaderBOffset
		logger.Infof("Stripe %d: Meta A invalid, Meta B valid. Using B.", s.ID)
	} else {
		// Both valid, pick the one with larger SyncSerial
		// Handle wrap around? Assuming serial increases.
		// TODO: better wrap around handling
		if headerA.SyncSerial >= headerB.SyncSerial {
			header = headerA
			headerOffset = s.HeaderAOffset
			logger.Infof("Stripe %d: Both valid. Using A (Serial %d >= %d)", s.ID, headerA.SyncSerial, headerB.SyncSerial)
		} else {
			header = headerB
			headerOffset = s.HeaderBOffset
			logger.Infof("Stripe %d: Both valid. Using B (Serial %d > %d)", s.ID, headerB.SyncSerial, headerA.SyncSerial)
		}
	}

	s.Header = header

	// 4. Read Dirs from chosen Meta
	dirSize := s.Dm.BinarySize()
	dirOffset := headerOffset + Offset(HeaderSize)
	dirRaw := make([]byte, dirSize)
	if _, err := s.Fp.ReadAt(dirRaw, int64(dirOffset)); err != nil {
		return err
	}

	// Verify Checksum
	dirsCheckSum := crc32.ChecksumIEEE(dirRaw)
	// logger.Infof("DirsCheckSum: %d, v.Header.dirsChecksum: %d", DirsCheckSum, s.Header.DirsChecksum)
	if dirsCheckSum != s.Header.DirsChecksum {
		return errors.New("invalid dir checksum")
	}

	// 5. Unmarshal Dirs
	if err := s.Dm.UnmarshalBinary(dirRaw); err != nil {
		return err
	}

	// 6. Restore State
	s.WritePos = Offset(s.Header.WritePos)
	if s.WritePos < s.DataOffset {
		s.WritePos = s.DataOffset
	}

	return nil
}

func (s *Stripe) readMeta(headerOff, footerOff Offset) (*StripeHeaderFooter, *StripeHeaderFooter, error) {
	// Read Header
	headerBuf := make([]byte, HeaderSize)
	if _, err := s.Fp.ReadAt(headerBuf, int64(headerOff)); err != nil {
		return nil, nil, err
	}
	header := &StripeHeaderFooter{}
	if err := header.UnmarshalBinary(headerBuf); err != nil {
		return nil, nil, err
	}

	// Read Footer
	footerBuf := make([]byte, HeaderSize)
	if _, err := s.Fp.ReadAt(footerBuf, int64(footerOff)); err != nil {
		return nil, nil, err
	}
	footer := &StripeHeaderFooter{}
	if err := footer.UnmarshalBinary(footerBuf); err != nil {
		return nil, nil, err
	}

	// Validate
	if header.Magic != MagicBocchi {
		return nil, nil, errors.New("invalid magic")
	}
	if header.WritePos != footer.WritePos || header.CreateUnixTime != footer.CreateUnixTime || header.SyncSerial != footer.SyncSerial {
		return nil, nil, errors.New("header and footer mismatch")
	}

	return header, footer, nil
}

func (s *Stripe) Close() error {
	// Flush pending writes in Aggregation Buffer
	// Note: we use StartOffset + WritePos because FlushSync expects the disk offset
	// where the CURRENT buffer (if flushed) would end? No, where it STARTS.
	// But WritePos points to where the next byte will be written.
	// AggBuffer tracks currentPos (bytes in buffer).
	// So StartOffset + WritePos is correct if WritePos was ADVANCED after writing to buffer.
	// In writeChunkLocked, s.WritePos += binLenOnDisk.
	// So WritePos is AHEAD of what is on disk (if buffer is not empty).
	// So StartOffset + WritePos is the END of the valid data stream including buffer.
	// FlushSync calculates start = diskOffset - currentPos.
	// So if we pass StartOffset + WritePos, start will be (StartOffset + WritePos) - currentPos.
	// This is correct because WritePos includes currentPos.

	if err := s.aggWriteBuffer.FlushSync(int64(s.StartOffset + s.WritePos)); err != nil {
		logger.Errorf("Stripe %d FlushSync failed: %v", s.ID, err)
		return err
	}
	// Close Aggregation Buffer (stops background loop)
	if err := s.aggWriteBuffer.Close(); err != nil {
		return err
	}
	// Flush Metadata
	return s.flushMetaToFp()
}

func (s *Stripe) flushMetaToFp() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// Increment Serial
	s.Header.SyncSerial++
	s.Header.WritePos = s.WritePos

	// Prepare Dirs Data
	dirRaw, err := s.Dm.MarshalBinary()
	if err != nil {
		return err
	}
	s.Header.DirsChecksum = crc32.ChecksumIEEE(dirRaw)

	headerData, err := s.Header.MarshalBinary()
	if err != nil {
		return err
	}

	// Decide where to write: A or B?
	// Serial 1 -> A, 2 -> B, 3 -> A...
	useA := s.Header.SyncSerial%2 != 0

	var headerOffset, footerOffset, dirOffset Offset
	if useA {
		headerOffset = s.HeaderAOffset
		footerOffset = s.FooterAOffset
		dirOffset = s.HeaderAOffset + Offset(HeaderSize)
	} else {
		headerOffset = s.HeaderBOffset
		footerOffset = s.FooterBOffset
		dirOffset = s.HeaderBOffset + Offset(HeaderSize)
	}

	// 1. Write Header
	if _, err := s.Fp.WriteAt(headerData, int64(headerOffset)); err != nil {
		return err
	}

	// 2. Write Dirs
	if _, err := s.Fp.WriteAt(dirRaw, int64(dirOffset)); err != nil {
		return err
	}

	// 3. Write Footer
	if _, err := s.Fp.WriteAt(headerData, int64(footerOffset)); err != nil {
		return err
	}

	// Sync file?
	// s.Fp.Sync() // heavy?

	return nil
}

func (s *Stripe) writeChunkLocked(ck *Chunk) (Offset, error) {

	binLenOnDisk := ck.GetBinaryLength()

	// Check for stripe overflow (Wrap around)
	// s.WritePos is relative to s.StartOffset
	if s.WritePos+Offset(binLenOnDisk) > s.Length {
		// Wrap around
		// Flush pending data
		if err := s.aggWriteBuffer.FlushSync(int64(s.StartOffset + s.WritePos)); err != nil {
			return 0, err
		}
		s.WritePos = s.DataOffset
	}

	chunkRelativeOffset := s.WritePos

	// Absolute offset on disk
	absOffset := s.StartOffset + s.WritePos

	err := ck.WriteAt(s.aggWriteBuffer, int64(absOffset))
	if err != nil {
		return 0, err
	}

	s.WritePos += Offset(binLenOnDisk)

	return chunkRelativeOffset, nil
}

// Helper to read chunk without stripe lock (but using internal locks/safety)
func (s *Stripe) readChunkInternal(offset Offset, size Offset) (*Chunk, error) {
	// 1. Try AggBuffer
	val, hit := s.loadFromAggregationBuffer(offset, size)
	if hit {
		ck := &Chunk{}
		if err := ck.UnmarshalBinary(val); err == nil {
			return ck, nil
		}
	}

	// 2. Disk + Overlay
	buf, err := s.loadFromDisk(offset, size)
	if err != nil {
		return nil, err
	}
	ck := &Chunk{}
	if err := ck.UnmarshalBinary(buf); err != nil {
		// Log binary data summary for debugging
		logger.Errorf("UnmarshalBinary failed. BufLen: %d, First 16 bytes: %x", len(buf), buf[:min(16, len(buf))])
		return nil, err
	}
	if err := ck.Verify(); err != nil {
		logger.Errorf("Verify failed. Key: %x, Magic: %x, Len: %d, TotalLen: %d, Checksum: %x",
			ck.Header.KeyHash, ck.Header.Magic, ck.Header.Len, ck.Header.TotalLen, ck.Header.Checksum)
		return nil, err
	}
	return ck, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// 1. LramHit
func (s *Stripe) loadFromRamCache(key []byte) (*CacheReader, bool) {
	if s.RamCache == nil {
		return nil, false
	}
	val, hit := s.RamCache.Get(key)
	if hit {
		return NewCacheReaderFromBytes(val), true
	}
	return nil, false
}

// 2. LmemHit
func (s *Stripe) loadFromAggregationBuffer(offset Offset, approxSize Offset) ([]byte, bool) {
	// approxSize already includes Doc + data (no padding)
	totalSize := int64(approxSize)
	absOffset := int64(s.StartOffset + offset)

	// Fast check: is it FULLY in buffer?
	if !s.aggWriteBuffer.IsFullyInCache(absOffset, totalSize) {
		return nil, false
	}

	buf := make([]byte, totalSize)
	// We know it's fully in cache, so CopyFrom will return true and full size
	s.aggWriteBuffer.CopyFrom(buf, absOffset)
	return buf, true
}

// 3. LdiskHit (with Overlay fallback)
func (s *Stripe) loadFromDisk(offset Offset, approxSize Offset) ([]byte, error) {
	absOffset := int64(s.StartOffset + offset)

	// Step 1: Read Doc header first to get exact TotalLen
	const docSize = 72 // binary.Size(Doc{})
	docBuf := make([]byte, docSize)

	s.Fp.ReadAt(docBuf, absOffset)
	s.aggWriteBuffer.CopyFrom(docBuf, absOffset)

	// Parse Doc to get TotalLen
	var doc Doc
	if err := binary.Read(bytes.NewReader(docBuf), binary.LittleEndian, &doc); err != nil {
		logger.Warnf("Failed to parse Doc header, using approxSize. Err: %v", err)
		// Fallback: use approxSize
		buf := make([]byte, int64(approxSize))
		s.Fp.ReadAt(buf, absOffset)
		s.aggWriteBuffer.CopyFrom(buf, absOffset)
		return buf, nil
	}

	if doc.TotalLen == 0 || doc.TotalLen > uint64(approxSize) {
		logger.Warnf("Doc.TotalLen invalid (%d), using approxSize (%d)", doc.TotalLen, approxSize)
		doc.TotalLen = uint64(approxSize)
	}

	// Step 2: Read full Chunk with exact size
	buf := make([]byte, doc.TotalLen)
	s.Fp.ReadAt(buf, absOffset)
	s.aggWriteBuffer.CopyFrom(buf, absOffset)

	return buf, nil
}

func (s *Stripe) checkSetRequest(key, value []byte) error {
	if len(key) > MaxKeyLength {
		return ErrChunkKeyTooLarge
	}
	return nil
}

func (s *Stripe) checkGetRequest(key []byte) error {
	if len(key) > MaxKeyLength {
		return ErrChunkKeyTooLarge
	}
	return nil
}
