package bakemono

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
)

// Chunk is the unit of data storage.
// Contains a header(meta) and data.
type Chunk struct {
	Header  Doc
	DataRaw []byte
}

// Set sets the key and data of the chunk.
func (c *Chunk) Set(key, data []byte) error {
	if len(data) > ChunkDataSize {
		return ErrChunkDataTooLarge
	}
	if len(key) > ChunkKeyMaxSize {
		return ErrChunkKeyTooLarge
	}
	c.DataRaw = data
	c.Header.Magic = MagicChunk
	docSize := binary.Size(c.Header)
	c.Header.Len = uint32(docSize + len(data)) // Total fragment length (Doc + data, unrounded)
	c.Header.Hlen = 0                          // No extended header for now
	c.Header.TotalLen = uint64(len(data))      // For single fragment, total_len equals data length
	c.Header.Checksum = crc32.ChecksumIEEE(data)

	// Set version
	c.Header.VMajor = 1
	c.Header.VMinor = 0
	c.Header.DocType = 0 // Default type

	// Initialize key hashes (will be set by caller if needed)
	// For now, compute hash from key
	if len(key) > 0 {
		h := md5.Sum(key)
		copy(c.Header.KeyHash[:], h[:])
		copy(c.Header.FirstKey[:], h[:]) // Default: same as key hash
	}

	return nil
}

// GetKeyData returns the key hash and data of the chunk.
// Note: Only KeyHash is stored, not the full key.
func (c *Chunk) GetKeyData() ([]byte, []byte) {
	return c.Header.KeyHash[:], c.DataRaw
}

// GetBinaryLength returns the binary length of the chunk (actual size, no padding).
func (c *Chunk) GetBinaryLength() Offset {
	docSize := binary.Size(c.Header)
	return Offset(docSize + len(c.DataRaw))
}

// WriteAt writes the chunk to the writer at the offset.
func (c *Chunk) WriteAt(w io.WriterAt, off int64) error {
	b, err := c.MarshalBinary()
	if err != nil {
		return err
	}
	_, err = w.WriteAt(b, off)
	return err
}

// ReadAt reads the chunk from the reader at the offset.
// Note: size should be the actual chunk size (Doc + data), not padded.
func (c *Chunk) ReadAt(r io.ReaderAt, off, size int64) error {
	data := make([]byte, size)
	_, err := r.ReadAt(data, off)
	if err != nil {
		return err
	}
	return c.UnmarshalBinary(data)
}

// Verify verifies the chunk. It returns nil if the chunk is valid.
func (c *Chunk) Verify() error {
	// magic check
	if c.Header.Magic != MagicChunk {
		return ErrChunkVerifyFailed
	}
	// data length check (using DataLen method)
	if len(c.DataRaw) != int(c.Header.DataLen()) {
		return ErrChunkVerifyFailed
	}
	// checksum check data
	if crc := crc32.ChecksumIEEE(c.DataRaw); crc != c.Header.Checksum {
		return ErrChunkVerifyFailed
	}
	return nil
}

// MarshalBinary returns the binary of the chunk (actual size, no padding).
func (c *Chunk) MarshalBinary() ([]byte, error) {
	b, err := c.Header.MarshalBinary()
	if err != nil {
		return nil, err
	}
	buf := bytes.NewBuffer(make([]byte, 0, len(b)+len(c.DataRaw)))
	buf.Write(b)
	buf.Write(c.DataRaw)
	return buf.Bytes(), nil
}

// UnmarshalBinary unmarshal the binary of the chunk, and verify it.
// Note: the data must be the whole chunk (Doc + data, no padding).
func (c *Chunk) UnmarshalBinary(data []byte) error {
	buf := bytes.NewBuffer(data)
	docSize := binary.Size(Doc{})
	if buf.Len() < docSize {
		return ErrChunkVerifyFailed
	}
	if err := c.Header.UnmarshalBinary(buf.Next(docSize)); err != nil {
		return err
	}
	c.DataRaw = buf.Next(int(c.Header.DataLen()))
	return c.Verify()
}

// Doc is the meta header of a chunk fragment, aligned with ATS Doc structure.
// Each cache fragment starts with a Doc header containing metadata.
type Doc struct {
	// Core fields (aligned with ATS Doc)
	Magic       uint32   // DOC_MAGIC - magic number for validation
	Len         uint32   // Total length of this fragment (including Doc + hlen + data)
	TotalLen    uint64   // Total length of the entire object (all fragments combined)
	FirstKey    [16]byte // First key of the object (shared by all fragments)
	KeyHash     [16]byte // Key hash for this fragment (16-byte hash for directory lookup)
	Hlen        uint32   // Length of extended header (HTTP headers, vector, etc.)
	DocType     uint8    // Document type (CACHE_FRAG_TYPE_HTTP, etc.)
	VMajor      uint8    // Major version number
	VMinor      uint8    // Minor version number
	Unused      uint8    // Unused, forced to zero
	SyncSerial  uint32   // Sync serial number
	WriteSerial uint32   // Write serial number
	Pinned      uint32   // Pinned until timestamp (prevents eviction)
	Checksum    uint32   // Checksum of data
}

// MarshalBinary returns the binary representation of the Doc header.
// TODO: could use a buffer pool to avoid allocating a new buffer every time.
func (d *Doc) MarshalBinary() ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.BigEndian, *d); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// UnmarshalBinary unmarshal the binary representation of the Doc header.
func (d *Doc) UnmarshalBinary(data []byte) error {
	return binary.Read(bytes.NewBuffer(data), binary.BigEndian, d)
}

// GenerateHeaderChecksum calculates checksum of header fields.
func (d *Doc) GenerateHeaderChecksum() uint32 {
	// Include all relevant fields in checksum
	return crc32.ChecksumIEEE([]byte(fmt.Sprintf("%v,%v,%v,%v,%v,%v,%v,%v,%v,%v,%v,%v",
		d.Magic, d.Len, d.TotalLen, d.FirstKey, d.KeyHash, d.Hlen,
		d.DocType, d.VMajor, d.VMinor, d.SyncSerial, d.WriteSerial, d.Pinned)))
}

// DataLen returns the length of data portion (excluding Doc header and extended header).
func (d *Doc) DataLen() uint32 {
	return d.Len - uint32(binary.Size(Doc{})) - d.Hlen
}

// SingleFragment returns true if this is a single fragment object.
func (d *Doc) SingleFragment() bool {
	return d.DataLen() == uint32(d.TotalLen)
}
