package cache

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
)

const (
	DocMagic      = 0x5F129B13
	DocCorrupt    = 0xDEADBABE
	DocNoChecksum = 0xA0B0C0D0
)

type Doc struct {
	Magic       uint32   // DOC_MAGIC
	Len         uint32   // length of this fragment (including hlen & sizeof(Doc), unrounded)
	TotalLen    uint64   // total length of document
	FirstKey    [16]byte // first key in object.
	Key         [16]byte // Key for this doc.
	HLen        uint32   // Length of this header.
	DocType     uint8    // Doc type
	VMajor      uint8    // Major version number.
	VMinor      uint8    // Minor version number.
	Unused      uint8    // Unused, forced to zero.
	SyncSerial  uint32
	WriteSerial uint32
	Pinned      uint32 // pinned until
	Checksum    uint32
}

const DocHeaderSize = 72 // Size of Doc struct in bytes

// Ensure DocHeaderSize matches binary.Size(Doc{})
func init() {
	if binary.Size(Doc{}) != DocHeaderSize {
		panic(fmt.Sprintf("Doc struct size mismatch: expected %d, got %d", DocHeaderSize, binary.Size(Doc{})))
	}
}

// Header returns the start of the header data (after the Doc struct).
// In Go, this would just be the data slice passed separately usually, but here we might operate on a buffer.
// This helper assumes usage where we have a full buffer containing Doc + Header + Data.

// CalculateChecksum covers the identity, lengths, version, metadata and body.
func (d *Doc) CalculateChecksum(data []byte) uint32 {
	if d.Len < DocHeaderSize || uint64(d.Len-DocHeaderSize) > uint64(len(data)) {
		return 0
	}
	header := *d
	header.Checksum = 0
	encoded, _ := header.MarshalBinary()
	sum := crc32.Update(0, crc32.IEEETable, encoded)
	return crc32.Update(sum, crc32.IEEETable, data[:d.Len-DocHeaderSize])
}

func (d *Doc) validate(data []byte) error {
	if d.Magic != DocMagic || d.VMajor != docIdentityVersion || d.VMinor != 0 || d.Unused != 0 {
		return fmt.Errorf("unsupported cache document")
	}
	if d.Len < DocHeaderSize || d.Len > AggBufferSize || uint64(d.Len) > uint64(len(data)) || d.HLen > d.Len-DocHeaderSize || d.DataLen() > TargetFragmentSize {
		return fmt.Errorf("invalid cache fragment length")
	}
	if d.Checksum != d.CalculateChecksum(data[DocHeaderSize:]) {
		return fmt.Errorf("cache document checksum mismatch")
	}
	return nil
}

func (d *Doc) MarshalBinary() ([]byte, error) {
	buf := new(bytes.Buffer)
	err := binary.Write(buf, binary.BigEndian, d)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (d *Doc) UnmarshalBinary(data []byte) error {
	buf := bytes.NewReader(data)
	err := binary.Read(buf, binary.BigEndian, d)
	return err
}

func (d *Doc) DataLen() uint32 {
	return d.Len - uint32(DocHeaderSize) - d.HLen
}

func (d *Doc) PrefixLen() uint32 {
	return uint32(DocHeaderSize) + d.HLen
}

func (d *Doc) SingleFragment() bool {
	return uint64(d.DataLen()) == d.TotalLen
}
