package cache

import (
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sync"
	"unsafe"

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

	// rw mutex for each segment
	SegMutexes []sync.RWMutex
}

// Init initializes the dir manager. Dirs will Initialized as empty by default.
func (dm *DirManager) Init(dirNum Offset) Offset {
	dm.BucketsNum = dirNum / DirDepth
	dm.SegmentsNum = (dm.BucketsNum + MaxBucketsPerSegment - 1) / MaxBucketsPerSegment
	dm.BucketsNumPerSegment = (dm.BucketsNum + dm.SegmentsNum - 1) / dm.SegmentsNum

	dm.FragmentsNum = dm.BucketsNumPerSegment * DirDepth * dm.SegmentsNum

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
	return hit, dirOffset, d
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
		if counter > 10000 {
			logger.Errorf("dirProbe: counter: %d, index: %d", counter, index)
			panic("dirProbe: counter>10000")
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
	return dm.BucketsNumPerSegment*DirDepth*Offset(segmentId) + offset, nil
}

func (dm *DirManager) dirInsert(key uint16, segmentId segId, bucketId Offset, dir Dir) (dirOffset Offset, err error) {
	dirs := dm.getSegmentDirs(segmentId)
	hit, dirOffset, _, _ := dirProbe(key, bucketId, dirs)
	if hit {
		// Note: set manually is dangerous, need to keep the next chain
		next := dirs[dirOffset].next()
		prev := dirs[dirOffset].prev()

		dirs[dirOffset] = dir
		dirs[dirOffset].setNext(next)
		dirs[dirOffset].setPrev(prev)
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

	if dirOffset == bucketId*DirDepth {
		// Deleting the head of the bucket
		next := Offset(dirs[dirOffset].next())
		if next != 0 {
			// Move the next entry to the head
			dirs[dirOffset] = dirs[next]
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

// MarshalBinary converts the Dirs to binary format using unsafe for zero-copy
func (dm *DirManager) MarshalBinary() (data []byte, err error) {
	segmentDirCount := int(dm.BucketsNumPerSegment) * DirDepth
	segmentSize := segmentDirCount*DirSize + DirIDSize
	totalSize := int(dm.SegmentsNum) * segmentSize

	data = make([]byte, totalSize)
	offset := 0

	for i := segId(0); Offset(i) < dm.SegmentsNum; i++ {
		dm.SegMutexes[i].Lock()
		dirs := dm.getSegmentDirs(i)

		// 1. Copy Dirs using unsafe
		// Pointer to start of this segment's dirs in the flat array
		ptr := unsafe.Pointer(&dirs[0])
		byteLen := segmentDirCount * DirSize

		// Create a temporary byte slice header for the source memory
		var srcBytes []byte
		sh := (*reflect.SliceHeader)(unsafe.Pointer(&srcBytes))
		sh.Data = uintptr(ptr)
		sh.Len = byteLen
		sh.Cap = byteLen

		// Copy to the output buffer
		copy(data[offset:], srcBytes)
		offset += byteLen

		// 2. Write DirFreeStart (uint16)
		// Using Little Endian to match typical memory layout on x86, assuming we want consistency with unsafe copy
		// if we were on LE machine.
		// However, for strict compatibility with previous BigEndian code, unsafe copy is breaking anyway (on LE).
		// So we just write it directly.
		val := dm.DirFreeStart[i]
		data[offset] = byte(val)
		data[offset+1] = byte(val >> 8)
		offset += 2

		dm.SegMutexes[i].Unlock()
	}
	return data, nil
}

func (dm *DirManager) UnmarshalBinary(data []byte) (err error) {
	segmentDirCount := int(dm.BucketsNumPerSegment) * DirDepth
	segmentSize := segmentDirCount*DirSize + DirIDSize
	if len(data) != int(dm.SegmentsNum)*segmentSize {
		return fmt.Errorf("invalid data size")
	}

	offset := 0
	for i := segId(0); Offset(i) < dm.SegmentsNum; i++ {
		dm.SegMutexes[i].Lock()
		dirs := dm.getSegmentDirs(i)

		// 1. Copy Dirs using unsafe
		byteLen := segmentDirCount * DirSize
		ptr := unsafe.Pointer(&dirs[0])

		var dstBytes []byte
		sh := (*reflect.SliceHeader)(unsafe.Pointer(&dstBytes))
		sh.Data = uintptr(ptr)
		sh.Len = byteLen
		sh.Cap = byteLen

		copy(dstBytes, data[offset:offset+byteLen])
		offset += byteLen

		// 2. Read DirFreeStart
		val := uint16(data[offset]) | uint16(data[offset+1])<<8
		dm.DirFreeStart[i] = val
		offset += 2

		dm.SegMutexes[i].Unlock()
	}
	return nil
}

func (dm *DirManager) UnmarshalBinaryConcurrency(data []byte) (err error) {
	segmentDirCount := int(dm.BucketsNumPerSegment) * DirDepth
	segmentSize := segmentDirCount*DirSize + DirIDSize
	if len(data) != int(dm.SegmentsNum)*segmentSize {
		return fmt.Errorf("invalid data size")
	}

	var wg sync.WaitGroup
	errChan := make(chan error, dm.SegmentsNum)
	for i := segId(0); Offset(i) < dm.SegmentsNum; i++ {
		wg.Add(1)
		go func(i segId) {
			defer wg.Done()
			dm.SegMutexes[i].Lock()
			defer dm.SegMutexes[i].Unlock()

			offset := int(i) * segmentSize
			dirs := dm.getSegmentDirs(i)

			// 1. Copy Dirs
			byteLen := segmentDirCount * DirSize
			ptr := unsafe.Pointer(&dirs[0])

			var dstBytes []byte
			sh := (*reflect.SliceHeader)(unsafe.Pointer(&dstBytes))
			sh.Data = uintptr(ptr)
			sh.Len = byteLen
			sh.Cap = byteLen

			copy(dstBytes, data[offset:offset+byteLen])
			offset += byteLen

			// 2. Read DirFreeStart
			val := uint16(data[offset]) | uint16(data[offset+1])<<8
			dm.DirFreeStart[i] = val
		}(i)
	}
	wg.Wait()
	close(errChan)
	// We don't really push to errChan in this version as copy doesn't error
	// But keeping channel structure if we add logic later
	return nil
}

func (dm *DirManager) BinarySize() int {
	metaSizePerSegment := int(dm.BucketsNumPerSegment*DirDepth*Offset(DirSize)) + int(DirIDSize)
	return int(dm.SegmentsNum) * metaSizePerSegment
}
