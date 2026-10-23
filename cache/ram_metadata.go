package cache

// ramEntry owns immutable serialized bytes and optional decoded HTTP metadata.
// Decoding is an admission-time optimization; readers still validate CRC/identity.
type ramEntry struct {
	data     []byte
	info     *CacheHTTPInfo
	checksum uint32
	weight   uint32
}

func newRAMEntry(data []byte) *ramEntry {
	e := &ramEntry{data: data, weight: uint32(max(1, len(data)))}
	if len(data) < DocHeaderSize {
		return e
	}
	var doc Doc
	if doc.UnmarshalBinary(data[:DocHeaderSize]) != nil || doc.Magic != DocMagic || doc.HLen == 0 || uint64(doc.HLen) > uint64(len(data)-DocHeaderSize) {
		return e
	}
	info := NewCacheHTTPInfo()
	if info.UnmarshalBinary(data[DocHeaderSize:DocHeaderSize+int(doc.HLen)]) != nil {
		return e
	}
	// Conservative decoded-map/string storage estimate, charged in both policies.
	charge := uint64(e.weight) + 256 + uint64(len(info.RequestMethod)+len(info.RequestURL))
	for name, values := range info.ResponseHeaders {
		charge += uint64(160 + len(name) + len(values)*32)
		for _, value := range values {
			charge += uint64(len(value))
		}
	}
	if charge > uint64(^uint32(0)) {
		return e
	}
	e.info, e.checksum, e.weight = info, doc.Checksum, uint32(charge)
	return e
}

func (h *CacheHTTPInfo) clone() *CacheHTTPInfo {
	out := *h
	out.ResponseHeaders = h.ResponseHeaders.Clone()
	return &out
}
