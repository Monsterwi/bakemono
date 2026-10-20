package cache

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Monsterwi/razor/logger"
)

const checkpointMagic uint64 = 0x424b434845434b33
const checkpointVersion uint32 = 1

type checkpointHeader struct {
	Magic                         uint64
	Version, Clean                uint32
	Skip, Length, Start, WritePos int64
	Phase                         uint32
	DirBytes                      uint64
	Entries                       uint32
	MapBytes                      uint64
	Session                       [16]byte
	Checksum                      uint32
}

func (s *Stripe) checkpointPath() string { return fmt.Sprintf("%s.%d.checkpoint", s.Path, s.Skip) }

// Atomic replacement keeps either the previous complete marker or the new one.
func (s *Stripe) publishCheckpoint(data []byte) error {
	path := s.checkpointPath()
	f, err := os.CreateTemp(filepath.Dir(path), ".bakemono-checkpoint-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = writeFullAt(f, data, 0); err == nil {
		err = f.Sync()
	}
	errClose := f.Close()
	if err == nil {
		err = errClose
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Stripe) encodeCheckpoint(clean bool) ([]byte, error) {
	h := checkpointHeader{Magic: checkpointMagic, Version: checkpointVersion, Skip: s.Skip, Length: s.Len, Start: s.Start, WritePos: s.WritePos, Session: s.session}
	if s.Phase {
		h.Phase = 1
	}
	var dirs, mapping []byte
	if clean {
		h.Clean = 1
		var err error
		dirs, err = s.DirMgr.MarshalBinary()
		if err != nil {
			return nil, err
		}
		entries := s.DirMgr.DirTrie.Snapshot()
		var buf bytes.Buffer
		for _, entry := range entries {
			if len(entry.Key) > MaxIndexKeyBytes || len(entry.Selector) > MaxIndexKeyBytes {
				return nil, fmt.Errorf("checkpoint key limit")
			}
			_ = binary.Write(&buf, binary.BigEndian, uint32(len(entry.Key)))
			_ = binary.Write(&buf, binary.BigEndian, uint32(len(entry.Selector)))
			buf.WriteString(entry.Key)
			buf.WriteString(entry.Selector)
		}
		mapping = buf.Bytes()
		if len(entries) > MaxIndexEntries || len(mapping) > MaxIndexBytes+8*MaxIndexEntries {
			return nil, fmt.Errorf("checkpoint mapping limit")
		}
		h.DirBytes = uint64(len(dirs))
		h.Entries = uint32(len(entries))
		h.MapBytes = uint64(len(mapping))
	}
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.BigEndian, h)
	buf.Write(dirs)
	buf.Write(mapping)
	data := buf.Bytes()
	binary.BigEndian.PutUint32(data[binary.Size(h)-4:], crc32.ChecksumIEEE(data))
	return data, nil
}

// Caller holds ioMu. Only drained Close may make a checkpoint recoverable.
func (s *Stripe) saveSnapshot(clean bool) error {
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
	data, err := s.encodeCheckpoint(clean)
	f := s.disk
	s.mu.Unlock()
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = s.publishCheckpoint(data)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failLocked(err)
}

func (s *Stripe) Load() error {
	started := time.Now()
	err := s.loadCheckpoint()
	outcome := "clean"
	if err != nil {
		outcome = "cold"
		s.DirMgr.InitEmptyDirs()
		_ = s.DirMgr.DirTrie.Restore(nil)
		s.WritePos = s.Start
		s.Phase = false
		s.Header = nil
	}
	logger.Infof("cache recovery outcome=%s format=%d duration=%s", outcome, checkpointVersion, time.Since(started))
	return err
}

func (s *Stripe) loadCheckpoint() error {
	f, err := os.Open(s.checkpointPath())
	if err != nil {
		return err
	}
	defer f.Close()
	var h checkpointHeader
	if err = binary.Read(f, binary.BigEndian, &h); err != nil {
		return err
	}
	headerSize := binary.Size(h)
	marker := make([]byte, StripeHeaderSize)
	if _, err = s.disk.ReadAt(marker, s.Skip); err != nil {
		return err
	}
	if binary.BigEndian.Uint32(marker) != StripeHeaderMagic || binary.BigEndian.Uint32(marker[4:]) != StripeHeaderVer || !bytes.Equal(marker[8:], h.Session[:]) {
		return fmt.Errorf("checkpoint session mismatch")
	}
	if h.Magic != checkpointMagic || h.Version != checkpointVersion || h.Clean != 1 || h.Skip != s.Skip || h.Length != s.Len || h.Start != s.Start || h.WritePos < s.Start || h.WritePos > s.Skip+s.Len || h.Phase > 1 || h.DirBytes != uint64(s.DirMgr.BinarySize()) || h.Entries > uint32(min(int(s.DirMgr.FragmentsNum), MaxIndexEntries)) || h.MapBytes > MaxIndexBytes+8*MaxIndexEntries {
		return fmt.Errorf("untrusted checkpoint")
	}
	size := uint64(headerSize) + h.DirBytes + h.MapBytes
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	if uint64(stat.Size()) != size {
		return fmt.Errorf("checkpoint length mismatch")
	}
	data := make([]byte, int(size))
	if _, err = f.ReadAt(data, 0); err != nil {
		return err
	}
	clear(data[headerSize-4 : headerSize])
	if crc32.ChecksumIEEE(data) != h.Checksum {
		return fmt.Errorf("checkpoint checksum mismatch")
	}
	mapping := bytes.NewReader(data[headerSize+int(h.DirBytes):])
	entries := make([]IndexEntry, 0, h.Entries)
	for i := uint32(0); i < h.Entries; i++ {
		var keyLen, selectorLen uint32
		if binary.Read(mapping, binary.BigEndian, &keyLen) != nil || binary.Read(mapping, binary.BigEndian, &selectorLen) != nil || keyLen == 0 || keyLen > MaxIndexKeyBytes || selectorLen > MaxIndexKeyBytes || uint64(keyLen)+uint64(selectorLen) > uint64(mapping.Len()) {
			return fmt.Errorf("invalid checkpoint key length")
		}
		key := make([]byte, keyLen)
		selector := make([]byte, selectorLen)
		_, _ = io.ReadFull(mapping, key)
		_, _ = io.ReadFull(mapping, selector)
		entries = append(entries, IndexEntry{Key: string(key), Selector: string(selector)})
	}
	if mapping.Len() != 0 {
		return fmt.Errorf("trailing checkpoint mapping")
	}
	if err = s.DirMgr.DirTrie.Restore(entries); err != nil {
		return err
	}
	if err = s.DirMgr.UnmarshalBinary(data[headerSize : headerSize+int(h.DirBytes)]); err != nil {
		return err
	}
	s.WritePos = h.WritePos
	s.Phase = h.Phase == 1
	s.Header = &StripeHeader{Magic: StripeHeaderMagic, Version: StripeHeaderVer, WritePos: s.WritePos, Phase: s.Phase}
	// Install only complete objects; raw fragment scans cannot resurrect old heads.
	type recoveredDir struct {
		key []byte
		dir Dir
	}
	recovered := make([]recoveredDir, 0)
	valid := make([]IndexEntry, 0, len(entries))
	for _, entry := range entries {
		key := []byte(entry.Key)
		r := NewCacheReader(s, key)
		if _, err = r.LoadHTTPInfo(); err != nil {
			continue
		}
		fragments := max(1, (r.totalLen+TargetFragmentSize-1)/TargetFragmentSize)
		if fragments > uint64(s.DirMgr.FragmentsNum) {
			continue
		}
		if _, err = io.Copy(io.Discard, r); err != nil {
			continue
		}
		for idx := 0; idx < int(fragments); idx++ {
			k := FragmentKey(key, idx)
			hit, _, d := s.DirMgr.Get(k)
			if !hit {
				return fmt.Errorf("checkpoint changed during recovery")
			}
			recovered = append(recovered, recoveredDir{k, d})
		}
		valid = append(valid, entry)
	}
	s.DirMgr.InitEmptyDirs()
	for _, r := range recovered {
		if _, err = s.DirMgr.Set(r.key, Offset(r.dir.offset()), int(r.dir.approxSize()), r.dir.phase()); err != nil {
			return err
		}
	}
	s.EvacMgr = NewEvacuationTable()
	return s.DirMgr.DirTrie.Restore(valid)
}

// A fresh durable nonce invalidates clean sidecars reached through any path alias.
func (s *Stripe) markSession() error {
	if _, err := rand.Read(s.session[:]); err != nil {
		return err
	}
	marker := make([]byte, StripeHeaderSize)
	binary.BigEndian.PutUint32(marker, StripeHeaderMagic)
	binary.BigEndian.PutUint32(marker[4:], StripeHeaderVer)
	copy(marker[8:], s.session[:])
	if err := writeFullAt(s.disk, marker, s.Skip); err != nil {
		return err
	}
	return s.disk.Sync()
}
