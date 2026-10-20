package cache

import (
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"sync"

	"github.com/Monsterwi/razor/logger"
)

type Offset uint64
type segId uint64

var (
	DirSize   = binary.Size(&Dir{})
	DirIDSize = binary.Size(uint16(0))
)

// DirManager manages the dirs attached to a vol.
type DirManager struct {
	FragmentsNum         Offset
	SegmentsNum          Offset
	BucketsNum           Offset
	BucketsNumPerSegment Offset

	// Dirs is a flat array of all dirs
	Dirs         []Dir
	DirFreeStart []uint16
	identities   [][16]byte

	DirTrie *DirTrie

	// rw mutex for each segment
	SegMutexes []sync.RWMutex
}

// Init initializes the dir manager. Dirs will Initialized as empty by default.
func (dm *DirManager) Init(dirNum Offset) Offset {
	dm.BucketsNum = dirNum / DirDepth
	dm.SegmentsNum = (dm.BucketsNum + MaxBucketsPerSegment - 1) / MaxBucketsPerSegment
	dm.BucketsNumPerSegment = (dm.BucketsNum + dm.SegmentsNum - 1) / dm.SegmentsNum

	dm.FragmentsNum = dm.BucketsNumPerSegment * DirDepth * dm.SegmentsNum

	dm.DirTrie = NewDirTrie(int(dm.FragmentsNum))
	dm.identities = make([][16]byte, dm.FragmentsNum)
	dm.Dirs = make([]Dir, dm.FragmentsNum)
	dm.DirFreeStart = make([]uint16, dm.SegmentsNum)
	dm.SegMutexes = make([]sync.RWMutex, dm.SegmentsNum)

	for i := 0; i < int(dm.SegmentsNum); i++ {
		dm.SegMutexes[i] = sync.RWMutex{}
	}

	dm.InitEmptyDirs()
	logger.Infof("initing dir manager: ChunksMaxNum: %d, BucketsNum: %d, SegmentsNum: %d, BucketsNumPerSegment: %d", dm.FragmentsNum, dm.BucketsNum, dm.SegmentsNum, dm.BucketsNumPerSegment)
	return dm.FragmentsNum
}

func (dm *DirManager) getSegmentDirs(seg segId) []Dir {
	start := Offset(seg) * dm.BucketsNumPerSegment * DirDepth
	end := start + dm.BucketsNumPerSegment*DirDepth
	return dm.Dirs[start:end]
}

// InitEmptyDirs initializes all dirs as empty, make chain.
func (dm *DirManager) InitEmptyDirs() {
	clear(dm.identities)
	for seg := 0; seg < int(dm.SegmentsNum); seg++ {
		dirs := dm.getSegmentDirs(segId(seg))

		// first free chunk for conclusion
		dm.DirFreeStart[seg] = 1

		// init all dirs as empty
		for i := 0; i < len(dirs); i++ {
			dirs[i] = Dir{}
		}

		// link dirs with next chain
		err := linkEmptyDirs(dirs)
		if err != nil {
			// should not happen
			logger.Fatal(err)
		}
	}
}

func linkEmptyDirs(dirs []Dir) error {

	if len(dirs)%DirDepth != 0 {
		return errors.New("dirs length should be multiple of DirDepth")
	}

	buckets := len(dirs) / DirDepth

	// link dirs with next chain
	for bucket := 0; bucket < buckets; bucket++ {
		for depth := 1; depth < DirDepth-1; depth++ {
			offset := bucket*DirDepth + depth
			dirs[offset].setNext(uint16(offset + 1))
		}
		if bucket != int(buckets)-1 {
			offset := bucket*DirDepth + DirDepth - 1
			dirs[offset].setNext(uint16(offset + 2))
		}
	}

	// link dirs with prev chain
	for bucket := 0; bucket < buckets; bucket++ {
		for depth := DirDepth - 1; depth > 1; depth-- {
			offset := bucket*DirDepth + depth
			dirs[offset].setPrev(uint16(offset - 1))
		}
		if bucket != 0 {
			offset := bucket*DirDepth + 1
			// first bucket - chunk 1 has no prev
			if offset != 1 {
				dirs[offset].setPrev(uint16(offset - 2))
			}
		}
	}
	return nil
}

// freeChainDelete deletes a free dir from the chain
func (dm *DirManager) freeChainDelete(segmentId segId, dirOffset Offset) {
	isFirst, next := freeChainDelete(dm.getSegmentDirs(segmentId), dirOffset)
	if isFirst {
		dm.DirFreeStart[segmentId] = next
	}
	return
}

func freeChainDelete(dirs []Dir, dirOffset Offset) (isFirst bool, freeListHead uint16) {
	if dirs[dirOffset].offset() != 0 {
		// TODO: remove panic once stable
		panic("dir is not empty")
	}
	prev := dirs[dirOffset].prev()
	if prev == 0 {
		isFirst = true
		freeListHead = dirs[dirOffset].next()
	} else {
		dirs[prev].setNext(dirs[dirOffset].next())
	}

	next := dirs[dirOffset].next()
	if next != 0 {
		dirs[next].setPrev(prev)
	}
	return
}

// freeChainSet set a free dir to the chain
func (dm *DirManager) freeChainSet(segmentId segId, dirOffset Offset) {
	dirs := dm.getSegmentDirs(segmentId)
	if dirs[dirOffset].offset() != 0 {
		// TODO: remove panic once stable
		panic("dir is not empty")
	}
	index := dm.DirFreeStart[segmentId]

	dirs[index].setPrev(uint16(dirOffset))
	dirs[dirOffset].setNext(index)
	dm.DirFreeStart[segmentId] = uint16(dirOffset)
}

// Get returns
// HIT: the offset of the dir entry with the given key,
// MISS: the offset of last dir entry in the bucket
func (dm *DirManager) Get(key []byte) (hit bool, dirOffset Offset, d Dir) {
	keyInt12, segmentId, bucketId := calcDirHashPosition(key, dm.SegmentsNum, dm.BucketsNumPerSegment)

	dm.SegMutexes[segmentId].RLock()
	defer dm.SegMutexes[segmentId].RUnlock()
	hit, dirOffset, _, d = dirProbe(keyInt12, bucketId, dm.getSegmentDirs(segmentId))
	global := dm.BucketsNumPerSegment*DirDepth*Offset(segmentId) + dirOffset
	if hit && dm.identities[global] != ([16]byte{}) && dm.identities[global] != docKey(key) {
		return false, dirOffset, Dir{}
	}
	return hit, global, d
}

func calcDirHashPosition(key []byte, SegmentsNum, BucketsNumPerSegment Offset) (keyInt12 uint16, segmentId segId, bucketId Offset) {
	h := md5.New()
	h.Write(key)
	keyHashed := h.Sum(nil)
	keyInt64 := binary.BigEndian.Uint64(keyHashed)
	keyInt12 = uint16(keyInt64 >> 52) // use high 12bit of md5 hash as keyInt16
	// use high 32bit of md5 hash as segment id, low 32bit as bucket id
	segmentId = segId(keyInt64>>32) % segId(SegmentsNum)
	bucketId = Offset(keyInt64&0xffffffff) % (BucketsNumPerSegment)
	return keyInt12, segmentId, bucketId
}

func dirProbe(key uint16, bucketId Offset, dirs []Dir) (hit bool, dirOffset, prevDirOffset Offset, d Dir) {
	index := bucketId * DirDepth
	prevIndex := Offset(0)
	counter := 0

	// do...while
	for index != 0 || counter == 0 {
		counter++
		if counter > len(dirs) || index >= Offset(len(dirs)) {
			return false, 0, 0, Dir{}
		}
		if dirs[index].offset() == 0 {
			return false, index, prevIndex, d
		}
		dirKey := dirs[index].tag()
		if dirKey == key {
			return true, index, prevIndex, dirs[index]
		}
		prevIndex = index
		index = Offset(dirs[index].next())
	}

	return false, index, prevIndex, d
}

func (dm *DirManager) Set(key []byte, off Offset, size int, phase bool) (dirOffset Offset, err error) {
	keyInt12, segmentId, bucketId := calcDirHashPosition(key, dm.SegmentsNum, dm.BucketsNumPerSegment)

	dir := Dir{}
	dir.setOffset(uint64(off))
	dir.setApproxSize(uint64(size))
	dir.setHead(true)
	dir.setTag(keyInt12)
	dir.setPhase(phase)

	dm.SegMutexes[segmentId].Lock()
	defer dm.SegMutexes[segmentId].Unlock()

	offset, err := dm.dirInsert(keyInt12, segmentId, bucketId, dir)
	if err != nil {
		return offset, err
	}
	global := dm.BucketsNumPerSegment*DirDepth*Offset(segmentId) + offset
	dm.identities[global] = docKey(key)
	return global, nil
}

func (dm *DirManager) dirInsert(key uint16, segmentId segId, bucketId Offset, dir Dir) (dirOffset Offset, err error) {
	dirs := dm.getSegmentDirs(segmentId)
	hit, dirOffset, _, _ := dirProbe(key, bucketId, dirs)
	if hit {
		// Note: set manually is dangerous, need to keep the next chain
		next := dirs[dirOffset].next()

		dirs[dirOffset] = dir
		dirs[dirOffset].setNext(next)
		return dirOffset, nil
	}

	//ddp := dm.DiagDumpAllDirsToString()
	//err = dm.DiagPanicHangUpDirs()
	//if err != nil {
	//	log.Printf("----------- diags: dumps all dirs no modify: \n%s", ddp)
	//	panic(err)
	//}

	// get a free dir
	_, freeDirOffset := dm.getFreeDir(segmentId, bucketId)

	// set dir
	dirs[freeDirOffset] = dir

	// link dir
	if freeDirOffset != bucketId*DirDepth {
		lastDirOfBucket := bucketId * DirDepth
		for dirs[lastDirOfBucket].next() != 0 {
			lastDirOfBucket = Offset(dirs[lastDirOfBucket].next())
		}
		dirs[lastDirOfBucket].setNext(uint16(freeDirOffset))
	}

	//err = dm.DiagPanicHangUpDirs()
	//if err != nil {
	//	log.Printf("----------- diags: dumps all dirs before: \n%s", ddp)
	//	log.Printf("----------- diags: dumps all dirs after: \n%s", dm.DiagDumpAllDirsToString())
	//	panic(err)
	//}
	return freeDirOffset, nil
}

func (dm *DirManager) Delete(key []byte) (dirOffset Offset, err error) {
	keyInt12, segmentId, bucketId := calcDirHashPosition(key, dm.SegmentsNum, dm.BucketsNumPerSegment)

	dm.SegMutexes[segmentId].Lock()
	defer dm.SegMutexes[segmentId].Unlock()

	hit, index, _, _ := dirProbe(keyInt12, bucketId, dm.getSegmentDirs(segmentId))
	global := dm.BucketsNumPerSegment*DirDepth*Offset(segmentId) + index
	if hit && dm.identities[global] != ([16]byte{}) && dm.identities[global] != docKey(key) {
		return 0, nil
	}
	offset, err := dm.dirDelete(keyInt12, segmentId, bucketId)
	if err != nil {
		return offset, err
	}
	return dm.BucketsNumPerSegment*DirDepth*Offset(segmentId) + offset, nil
}

func (dm *DirManager) dirDelete(key uint16, segmentId segId, bucketId Offset) (dirOffset Offset, err error) {
	dirs := dm.getSegmentDirs(segmentId)
	hit, dirOffset, prevDirOffset, _ := dirProbe(key, bucketId, dirs)
	if !hit {
		return dirOffset, nil
	}

	base := dm.BucketsNumPerSegment * DirDepth * Offset(segmentId)
	dm.identities[base+dirOffset] = [16]byte{}
	if dirOffset == bucketId*DirDepth {
		// Deleting the head of the bucket
		next := Offset(dirs[dirOffset].next())
		if next != 0 {
			// Move the next entry to the head
			dirs[dirOffset] = dirs[next]
			dm.identities[base+dirOffset] = dm.identities[base+next]
			dm.identities[base+next] = [16]byte{}
			dirs[next].clear()
			dm.freeChainSet(segmentId, next)
		} else {
			// No more entries in the bucket
			dirs[dirOffset].clear()
		}
		return dirOffset, nil
	}

	// Deleting a non-head entry
	next := dirs[dirOffset].next()
	dirs[prevDirOffset].setNext(next)

	dirs[dirOffset].clear()

	// link the dir to the free chain
	dm.freeChainSet(segmentId, dirOffset)
	return dirOffset, nil
}

func (dm *DirManager) getFreeDir(segmentId segId, bucketId Offset) (isSameBucket bool, freeDirOffset Offset) {
	dirs := dm.getSegmentDirs(segmentId)
	index := bucketId * DirDepth
	// head of bucket
	if dirs[index].offset() == 0 {
		return true, index
	}
	// same bucket
	for i := 1; i < DirDepth; i++ {
		if dirs[index+Offset(i)].offset() == 0 {
			dm.freeChainDelete(segmentId, index+Offset(i))
			return true, index + Offset(i)
		}
	}
	// pop a free dir from free chain
	return false, dm.freeChainPop(segmentId, bucketId)
}

func (dm *DirManager) freeChainPop(segmentId segId, whileListBucketId Offset) (freeDirOffset Offset) {
	loop := 0
FindFreeDir:
	loop++
	if loop > 49 {
		purgedNum := dm.purgeRandom100(segmentId, whileListBucketId)
		logger.Errorf("dirFreeChainPop: loop too much, purge 100: %d dirs", purgedNum)
	}
	if loop > 50 {
		// should not happen after many purge
		// TODO: remove panic once stable
		panic("freeChainPop: loop>50")
	}
	index := dm.DirFreeStart[segmentId]
	// no available free dir
	if index == 0 {
		// try to rebuild the free chain
		foundFreeDir := dm.freeChainRebuild(segmentId)
		//log.Printf("dirFreeChainPop: no free dir, rebuild %d dirs", foundFreeDir)

		// purge some dirs
		if foundFreeDir == 0 {
			//purgedNum := dm.purgeRandom10(segmentId, whileListBucketId)
			_ = dm.purgeRandom10(segmentId, whileListBucketId)
			//log.Printf("dirFreeChainPop: no free dir, purge %d dirs", purgedNum)
			goto FindFreeDir
		}
	}

	// pop the first free dir
	index = dm.DirFreeStart[segmentId]
	freeDirOffset = Offset(index)
	dm.freeChainDelete(segmentId, freeDirOffset)
	return
}

// freeChainRebuild rebuilds the free chain in a segment, try to reuse free dirs
func (dm *DirManager) freeChainRebuild(segmentId segId) Offset {
	dirs := dm.getSegmentDirs(segmentId)
	dm.DirFreeStart[segmentId] = 0
	prev := Offset(0)
	counter := Offset(0)

	for b := 0; b < int(dm.BucketsNumPerSegment); b++ {
		index := Offset(b) * DirDepth
		for j := 1; j < DirDepth; j++ {
			thisNodeIndex := index + Offset(j)
			if dirs[thisNodeIndex].offset() == 0 {
				counter++
				if dm.DirFreeStart[segmentId] == 0 {
					dm.DirFreeStart[segmentId] = uint16(thisNodeIndex)
				}
				dirs[thisNodeIndex].clear()
				dirs[thisNodeIndex].setPrev(uint16(prev))
				if prev != 0 {
					dirs[prev].setNext(uint16(thisNodeIndex))
				}
				prev = thisNodeIndex
			}
		}
	}

	//if prev != 0 {
	//	dm.Dirs[segmentId][prev].setNext(0)
	//}
	return counter
}

// purgeRandom10 purges 10% dirs in the segment randomly.
// if bucketsNumPerSegment < 10, purge all dirs
func (dm *DirManager) purgeRandom10(segmentId segId, whileListBucketId Offset) Offset {
	dirs := dm.getSegmentDirs(segmentId)
	randomIndex := rand.Intn(10)
	counter := 0
	for i := Offset(0); i < dm.BucketsNumPerSegment; i++ {
		if (dm.BucketsNumPerSegment > 10) && (i%10 != Offset(randomIndex)) {
			continue
		}
		if i == whileListBucketId {
			continue
		}
		// purge whole bucket
		index := Offset(i) * DirDepth
		c := 0 // do while
		for index != 0 || c == 0 {
			counter++
			c++

			next := Offset(dirs[index].next())
			dirs[index].clear()
			index = next
		}
	}

	dm.freeChainRebuild(segmentId)

	return Offset(counter)
}

// purgeRandom33 purges 33% dirs in the segment randomly.
// if bucketsNumPerSegment < 3, purge all dirs
func (dm *DirManager) purgeRandom33(segmentId segId, whileListBucketId Offset) Offset {
	dirs := dm.getSegmentDirs(segmentId)
	randomIndex := rand.Intn(3)
	counter := 0
	for i := Offset(0); i < dm.BucketsNumPerSegment; i++ {
		if (dm.BucketsNumPerSegment > 3) && (i%3 != Offset(randomIndex)) {
			continue
		}
		if i == whileListBucketId {
			continue
		}
		// purge whole bucket
		index := Offset(i) * DirDepth
		c := 0 // do while
		for index != 0 || c == 0 {
			counter++
			c++

			next := Offset(dirs[index].next())
			dirs[index].clear()
			index = next
		}
	}

	dm.freeChainRebuild(segmentId)

	return Offset(counter)
}

// purgeRandom100 purges 100% dirs in the segment randomly.
// if bucketsNumPerSegment < 3, purge all dirs
func (dm *DirManager) purgeRandom100(segmentId segId, whileListBucketId Offset) Offset {
	dirs := dm.getSegmentDirs(segmentId)
	counter := 0
	for i := Offset(0); i < dm.BucketsNumPerSegment; i++ {
		if i == whileListBucketId {
			continue
		}
		// purge whole bucket
		index := Offset(i) * DirDepth
		c := 0 // do while
		for index != 0 || c == 0 {
			counter++
			c++

			next := Offset(dirs[index].next())
			dirs[index].clear()
			index = next
		}
	}

	dm.freeChainRebuild(segmentId)

	return Offset(counter)
}

// MarshalBinary uses a fixed little-endian directory representation.
func (dm *DirManager) MarshalBinary() ([]byte, error) {
	data := make([]byte, dm.BinarySize())
	off := 0
	for seg := segId(0); Offset(seg) < dm.SegmentsNum; seg++ {
		dm.SegMutexes[seg].RLock()
		for _, d := range dm.getSegmentDirs(seg) {
			for _, v := range d.raw {
				binary.LittleEndian.PutUint16(data[off:], v)
				off += 2
			}
		}
		binary.LittleEndian.PutUint16(data[off:], dm.DirFreeStart[seg])
		off += 2
		dm.SegMutexes[seg].RUnlock()
	}
	return data, nil
}

func validateDirectory(dirs []Dir, free uint16) error {
	seen := make([]bool, len(dirs))
	for bucket := 0; bucket < len(dirs); bucket += DirDepth {
		i := bucket
		if dirs[i].offset() == 0 {
			if dirs[i].next() != 0 {
				return fmt.Errorf("empty directory bucket has chain")
			}
			continue
		}
		for {
			if i >= len(dirs) || seen[i] || (i != bucket && i%DirDepth == 0) || dirs[i].offset() == 0 {
				return fmt.Errorf("invalid directory chain")
			}
			seen[i] = true
			if dirs[i].next() == 0 {
				break
			}
			i = int(dirs[i].next())
		}
	}
	prev := uint16(0)
	for free != 0 {
		i := int(free)
		if i >= len(dirs) || seen[i] || i%DirDepth == 0 || dirs[i].offset() != 0 || dirs[i].prev() != prev {
			return fmt.Errorf("invalid directory free chain")
		}
		seen[i] = true
		prev = free
		free = dirs[i].next()
	}
	for i, d := range dirs {
		if !seen[i] && (i%DirDepth != 0 || d.offset() != 0) {
			return fmt.Errorf("unreachable directory entry")
		}
	}
	return nil
}

func (dm *DirManager) UnmarshalBinary(data []byte) error {
	if len(data) != dm.BinarySize() {
		return fmt.Errorf("invalid directory size")
	}
	dirs := make([]Dir, len(dm.Dirs))
	free := make([]uint16, len(dm.DirFreeStart))
	off := 0
	count := int(dm.BucketsNumPerSegment) * DirDepth
	for seg := range free {
		for j := seg * count; j < (seg+1)*count; j++ {
			for k := range dirs[j].raw {
				dirs[j].raw[k] = binary.LittleEndian.Uint16(data[off:])
				off += 2
			}
		}
		free[seg] = binary.LittleEndian.Uint16(data[off:])
		off += 2
		if err := validateDirectory(dirs[seg*count:(seg+1)*count], free[seg]); err != nil {
			return err
		}
	}
	for seg := range free {
		dm.SegMutexes[seg].Lock()
		copy(dm.getSegmentDirs(segId(seg)), dirs[seg*count:(seg+1)*count])
		dm.DirFreeStart[seg] = free[seg]
		clear(dm.identities[seg*count : (seg+1)*count])
		dm.SegMutexes[seg].Unlock()
	}
	return nil
}

func (dm *DirManager) UnmarshalBinaryConcurrency(data []byte) error { return dm.UnmarshalBinary(data) }

func (dm *DirManager) BinarySize() int {
	metaSizePerSegment := int(dm.BucketsNumPerSegment*DirDepth*Offset(DirSize)) + int(DirIDSize)
	return int(dm.SegmentsNum) * metaSizePerSegment
}

// deleteAt lets failed writers clean up only the fragments they published.
func (dm *DirManager) deleteAt(key []byte, expected int64) bool {
	tag, segment, bucket := calcDirHashPosition(key, dm.SegmentsNum, dm.BucketsNumPerSegment)
	dm.SegMutexes[segment].Lock()
	defer dm.SegMutexes[segment].Unlock()
	hit, index, _, dir := dirProbe(tag, bucket, dm.getSegmentDirs(segment))
	global := dm.BucketsNumPerSegment*DirDepth*Offset(segment) + index
	if hit && dm.identities[global] != ([16]byte{}) && dm.identities[global] != docKey(key) {
		return false
	}
	if !hit {
		return true
	}
	if int64(dir.offset()) != expected {
		return false
	}
	_, err := dm.dirDelete(tag, segment, bucket)
	return err == nil
}

// removePhase prevents old directory entries becoming valid after phase reuse.
// It runs once per wrap, rather than scanning the directory on every write.
func (dm *DirManager) removePhase(phase bool) {
	for seg := segId(0); Offset(seg) < dm.SegmentsNum; seg++ {
		dm.SegMutexes[seg].Lock()
		dirs := dm.getSegmentDirs(seg)
		for bucket := Offset(0); bucket < dm.BucketsNumPerSegment; bucket++ {
			index := bucket * DirDepth
			for {
				d := dirs[index]
				if d.offset() == 0 {
					break
				}
				if d.phase() == phase {
					_, _ = dm.dirDelete(d.tag(), seg, bucket)
					index = bucket * DirDepth
					continue
				}
				if d.next() == 0 {
					break
				}
				index = Offset(d.next())
			}
		}
		dm.SegMutexes[seg].Unlock()
	}
}
