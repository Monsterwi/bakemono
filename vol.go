package bakemono

import (
	"errors"
	"fmt"
	"os"
	"time"
)

type Offset uint64
type segId uint64

// Vol is a volume represents a file on disk.
// It manages multiple Stripes.
type Vol struct {
	Path       string
	Fp         *os.File
	Stripes    []*Stripe
	NumStripes int

	Length       Offset
	ChunkAvgSize Offset

	closeCh chan struct{}
}

// VolOptions to init a Vol.
// Note: do file open/truncate outside.
type VolOptions struct {
	Fp                OffsetReaderWriterCloser
	FileSize          Offset
	ChunkAvgSize      Offset
	RamCacheSizeMb    uint64
	FlushMetaInterval time.Duration
}

// NewDefaultVolOptions creates a VolOptions with a file path.
// Note: It will create a file if not exists, and truncate it to the given sizeInternal.
func NewDefaultVolOptions(path string, fileSize, avgChunkSize, ramCacheSizeMb uint64) (*VolOptions, error) {
	logger.Infof("creating vol options with file truncate, path: %s, fileSize: %d, avgChunkSize: %d", path, fileSize, avgChunkSize)
	fp, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, err
	}
	logger.Infof("file opened, try to truncate to sizeInternal: %d", fileSize)
	err = fp.Truncate(int64(fileSize))
	if err != nil {
		return nil, err
	}
	return &VolOptions{
		Fp:                fp,
		FileSize:          Offset(fileSize),
		ChunkAvgSize:      Offset(avgChunkSize),
		RamCacheSizeMb:    ramCacheSizeMb,
		FlushMetaInterval: 60 * time.Second,
	}, nil
}

// Check checks if the VolOptions is valid.
func (cfg *VolOptions) Check() error {
	if cfg.Fp == nil {
		return errors.New("invalid config: Fp is nil")
	}
	if cfg.FileSize == 0 {
		return errors.New("invalid config: FileSize is 0")
	}
	if cfg.ChunkAvgSize == 0 {
		return errors.New("invalid config: ChunkAvgSize is 0")
	}
	return nil
}

func (v *Vol) Init(cfg *VolOptions) (corrupted bool, err error) {
	logger.Infof("initing vol, config: %+v", cfg)
	if err = cfg.Check(); err != nil {
		return false, err
	}

	v.Fp = cfg.Fp.(*os.File)
	v.Length = cfg.FileSize
	v.ChunkAvgSize = cfg.ChunkAvgSize
	v.closeCh = make(chan struct{})

	if uint64(cfg.FileSize) < StripeBlockSize {
		return false, fmt.Errorf("file size %d is less than stripe block size %d", cfg.FileSize, StripeBlockSize)
	}

	numStripes := uint64(cfg.FileSize) / StripeBlockSize
	v.NumStripes = int(numStripes)
	v.Stripes = make([]*Stripe, v.NumStripes)

	for i := 0; i < v.NumStripes; i++ {
		start := int64(i) * int64(StripeBlockSize)
		length := int64(StripeBlockSize)
		if i == v.NumStripes-1 {
			length = int64(cfg.FileSize) - start
		}

		rcSize := cfg.RamCacheSizeMb * 1024 * 1024 / uint64(v.NumStripes)
		v.Stripes[i] = NewStripe(i, v.Fp, start, length, rcSize)

		corrupted, err := v.Stripes[i].Init(uint64(cfg.ChunkAvgSize))
		if err != nil {
			logger.Warnf("stripe %d init failed: %v, corrupted: %v, ignore this if first time running.", i, err, corrupted)
		}
	}

	go v.SyncFlushLoop(cfg.FlushMetaInterval)

	logger.Infof("init vol done")
	return false, nil
}

func (v *Vol) Close() error {
	close(v.closeCh)
	// Close all stripes (flush buffers)
	for _, s := range v.Stripes {
		// Close stripe (flush buffers and meta)
		if err := s.Close(); err != nil {
			logger.Errorf("Stripe %d Close failed: %v", s.ID, err)
		}
	}
	return v.Fp.Close()
}

func (v *Vol) SyncFlushLoop(interval time.Duration) {
	for {
		select {
		case <-v.closeCh:
			return
		case <-time.After(interval):
			v.flushMetaToFp()
		}
	}
}

func (v *Vol) flushMetaToFp() {
	for _, s := range v.Stripes {
		if err := s.flushMetaToFp(); err != nil {
			logger.Errorf("Stripe %d flush meta failed: %v", s.ID, err)
		}
	}
}
