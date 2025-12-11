package cache

import (
	"bytes"
	"encoding/binary"
	"fmt"
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

// CalculateChecksum calculates the checksum of the document data (Header + Data).
// It excludes the Doc struct itself.
func (d *Doc) CalculateChecksum(data []byte) uint32 {
	// data should be the content after the Doc struct (Header + Body)
	// ensure len(data) matches d.Len - DocHeaderSize
	expectedLen := int(d.Len) - DocHeaderSize
	if len(data) < expectedLen {
		return 0 // Or error? ATS assumes valid pointers.
	}

	// Use only the valid part of data corresponding to this fragment
	payload := data[:expectedLen]

	var sum uint32
	for _, b := range payload {
		sum += uint32(b)
	}
	return sum
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
