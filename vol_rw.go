package bakemono

const MaxKeyLength = 4096

func (v *Vol) Set(key, value []byte) (err error) {
	//log.Printf("DEBUG: set key: %s, value_len: %d", key, len(value))
	err = v.checkSetRequest(key, value)
	if err != nil {
		return err
	}

	ck := &Chunk{}
	err = ck.Set(key, value)
	if err != nil {
		return err
	}

	binLenOnDisk := ck.GetBinaryLength()
	if v.WritePos+binLenOnDisk > v.Length {
		logger.Infof("data write overflowed, start from dataOffset. set: writePos: %d, dataOffset: %d, len(value): %d", v.WritePos, v.DataOffset, len(value))
		v.aggBufFlush(true)
		v.WritePos = v.DataOffset
	}

	// big file direct write to disk
	if binLenOnDisk >= AggHighWaterMark {
		v.aggBufFlush(true)

		writeOffset := v.WritePos
		v.WritePos += binLenOnDisk

		v.Dm.Set(key, writeOffset, int(binLenOnDisk))
		err = ck.WriteAt(v.Fp, int64(writeOffset))
		if err != nil {
			return err
		}
	} else {
		v.aggWriteBuffer.mutex.Lock()
		defer v.aggWriteBuffer.mutex.Unlock()
		v.Dm.Set(key, v.WritePos+Offset(v.aggWriteBuffer.bufferPos), int(binLenOnDisk))
		err = ck.WriteAt(v.aggWriteBuffer, int64(v.aggWriteBuffer.bufferPos))
		if err != nil {
			return err
		}
		if v.aggWriteBuffer.bufferPos >= AggHighWaterMark {
			v.aggBufFlush(false)
		}
	}

	// write to ram cache
	v.RamCache.Put(key, value)
	return nil
}

func (v *Vol) checkSetRequest(key, value []byte) (err error) {
	if len(key) > MaxKeyLength {
		return ErrChunkKeyTooLarge
	}
	//if Offset(len(value)) > 10 * v.ChunkAvgSize {
	//	return ErrChunkDataTooLarge
	//}
	return nil
}

func (v *Vol) Get(key []byte) (hit bool, value []byte, err error) {
	err = v.checkGetRequest(key)
	if err != nil {
		return false, nil, err
	}

	// load from ram cache
	value, err = v.RamCache.Get(key)
	if value != nil {
		return true, value, nil
	}

	hit, _, d := v.Dm.Get(key)

	if !hit {
		return false, nil, nil
	}

	// read data
	readOffset := d.offset()
	approxSize := d.approxSize()

	rt := v.Fp
	v.aggWriteBuffer.mutex.RLock()
	defer v.aggWriteBuffer.mutex.RUnlock()
	if v.dirAggBufValid(d) {
		// load from aggregation buffer
		rt = v.aggWriteBuffer
		readOffset = readOffset - uint64(v.WritePos)
	}

	ck := &Chunk{}
	err = ck.ReadAt(rt, int64(readOffset), int64(approxSize))
	if err != nil {
		logger.Warnf("failed to read data chunk. key: %s, offset: %d, approxSize: %d, err: %s", key, readOffset, approxSize, err)
		return false, nil, nil
	}
	ckKey, ckData := ck.GetKeyData()
	if string(ckKey) != string(key) {
		logger.Warnf("key mismatch. key: %s, ckKey: %s", key, ckKey)
		return false, nil, nil
	}

	return true, ckData, nil
}

func (v *Vol) checkGetRequest(key []byte) (err error) {
	if len(key) > MaxKeyLength {
		return ErrChunkKeyTooLarge
	}
	return nil
}

func (v *Vol) aggBufFlush(lock bool) {
	if lock {
		v.aggWriteBuffer.mutex.Lock()
		defer v.aggWriteBuffer.mutex.Unlock()
	}
	if v.aggWriteBuffer.Empty() {
		return
	}

	n, err := v.aggWriteBuffer.Flush(int64(v.WritePos))
	if err != nil || n != v.aggWriteBuffer.bufferPos {
		logger.Errorf("flush to disk error, clear aggWriteBuffer dir")
		// delete dir
		v.dirAggBufDel()
	} else {
		v.WritePos += Offset(n)
	}
	v.aggWriteBuffer.Reset()
	// v.flushMetaToFp()
}

func (v *Vol) dirAggBufValid(d Dir) bool {
	return d.offset() >= uint64(v.WritePos) &&
		d.offset() < (uint64(v.WritePos)+uint64(v.aggWriteBuffer.bufferPos))
}

func (v *Vol) dirAggBufDel() {
	v.aggWriteBuffer.mutex.Lock()
	defer v.aggWriteBuffer.mutex.Unlock()

	data := make([]byte, ChunkHeaderSizeFixed)
	for off := 0; off < v.aggWriteBuffer.bufferPos; {
		v.aggWriteBuffer.ReadAt(data, int64(off))
		ckHeader := &ChunkHeader{}
		ckHeader.UnmarshalBinary(data)
		v.Dm.Delete(ckHeader.Key[:])
		off += ChunkHeaderSizeFixed + int(ckHeader.DataLength)
	}
}
