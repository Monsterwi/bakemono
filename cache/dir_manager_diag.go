package cache

import (
	"bytes"
	"fmt"

	"github.com/Monsterwi/razor/logger"
)

func (dm *DirManager) DiagHangUsedDirs() (int, error) {
	// mark dirs used and linked from head node
	var counter int
	var mpDirsUsed []map[int]bool
	for seg := 0; seg < int(dm.SegmentsNum); seg++ {
		dirs := dm.getSegmentDirs(segId(seg))
		mp := make(map[int]bool)
		for buck := 0; buck < int(dm.BucketsNumPerSegment); buck++ {
			index := buck * DirDepth
			for dirs[index].offset() != 0 {
				counter++
				mp[index] = true
				next := int(dirs[index].next())
				if next == 0 {
					break
				}
				index = next
				if counter > 100000000 {
					panic("DiagHangUsedDirs: counter > 100000000")
				}
			}
		}
		mpDirsUsed = append(mpDirsUsed, mp)
	}

	// check if dir hang-up
	for seg := 0; seg < int(dm.SegmentsNum); seg++ {
		dirs := dm.getSegmentDirs(segId(seg))
		for buck := 0; buck < int(dm.BucketsNumPerSegment); buck++ {
			index := buck * DirDepth
			for i := 0; i < DirDepth; i++ {
				if dirs[index+i].offset() != 0 {
					if !mpDirsUsed[seg][index+i] {
						return 0, fmt.Errorf("find a hang-up dir. seg: %d, buck: %d, index: %d", seg, buck, index+i)
					}
				}
			}
		}
	}
	return counter, nil
}

func (dm *DirManager) DiagHangFreeDirs() (int, error) {
	// mark dirs registered in free list
	var counter int
	var mpDirsInFreeList []map[uint16]bool
	for seg := 0; seg < int(dm.SegmentsNum); seg++ {
		dirs := dm.getSegmentDirs(segId(seg))
		mp := make(map[uint16]bool)
		index := dm.DirFreeStart[segId(seg)]

		for index != 0 {
			counter++
			mp[index] = true
			index = dirs[index].next()
			if counter > 100000000 {
				panic("DiagHangFreeDirs: counter > 100000000")
			}
		}
		mpDirsInFreeList = append(mpDirsInFreeList, mp)
	}

	// check if dir hang-up
	for seg := 0; seg < int(dm.SegmentsNum); seg++ {
		dirs := dm.getSegmentDirs(segId(seg))
		for buck := 0; buck < int(dm.BucketsNumPerSegment); buck++ {
			index := buck * DirDepth
			for i := 1; i < DirDepth; i++ {
				if dirs[index+i].offset() == 0 {
					if !mpDirsInFreeList[seg][uint16(index+i)] {
						return 0, fmt.Errorf("find a hang-up dir. seg: %d, buck: %d, index: %d", seg, buck, index+i)
					}
				}
			}
		}
	}
	return counter, nil
}

func (dm *DirManager) DiagDumpAllDirs() {
	logger.Infof("Dump all dirs, segments: %d, buckets: %d", dm.SegmentsNum, dm.BucketsNumPerSegment)
	for seg := 0; seg < int(dm.SegmentsNum); seg++ {
		dirs := dm.getSegmentDirs(segId(seg))
		logger.Infof("Segment %d", seg)
		for buck := 0; buck < int(dm.BucketsNumPerSegment); buck++ {
			logger.Infof("Bucket %d", buck)
			index := buck * DirDepth
			for i := 0; i < DirDepth; i++ {
				logger.Infof("Segment %d, \tBucket %d, \tDir %d, \toffset: %d, \tprev: %d, next: %d",
					seg, buck, index, dirs[index].offset(), dirs[index].prev(), dirs[index].next())
				index++
			}
		}
	}
}

func (dm *DirManager) DiagDumpAllDirsToString() string {
	bf := bytes.NewBufferString("")
	bf.WriteString(fmt.Sprintf("Dump all dirs, segments: %d, buckets: %d\n", dm.SegmentsNum, dm.BucketsNumPerSegment))
	for seg := 0; seg < int(dm.SegmentsNum); seg++ {
		dirs := dm.getSegmentDirs(segId(seg))
		//log.Printf("Segment %d", seg)
		for buck := 0; buck < int(dm.BucketsNumPerSegment); buck++ {
			//log.Printf("Bucket %d", buck)
			index := buck * DirDepth
			for i := 0; i < DirDepth; i++ {
				bf.WriteString(fmt.Sprintf(
					"Segment %d, \tBucket %d, \tDir %d, \toffset: %d, \tprev: %d, next: %d\n",
					seg, buck, index, dirs[index].offset(), dirs[index].prev(), dirs[index].next()))
				index++
			}
		}
	}
	return bf.String()
}

func (dm *DirManager) DiagPanicHangUpDirs() error {
	_, err := dm.DiagHangUsedDirs()
	if err != nil {
		logger.Errorf("dirInsert: DiagHangUsedDirs: %v", err)
		return err
	}
	return nil
}
