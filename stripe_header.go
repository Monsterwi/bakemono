package bakemono

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

type StripeHeaderFooter struct {
	Magic          uint32
	Version        uint32
	CreateUnixTime int64
	WritePos       Offset
	// LastWritePos   Offset
	// AggPos         Offset
	// Generation  uint32
	// Phase       uint32
	// Cycle       uint32
	SyncSerial  uint32
	WriteSerial uint32
	// Dirty       uint32

	DirsChecksum uint32
	Checksum     uint32
}

func (v *StripeHeaderFooter) GenerateChecksum() uint32 {
	return crc32.ChecksumIEEE([]byte(fmt.Sprintf("%v,%v,%v,%v,%v", v.Magic, v.Version, v.CreateUnixTime, v.WritePos, v.SyncSerial)))
}

func (v *StripeHeaderFooter) MarshalBinary() (data []byte, err error) {
	buf := &bytes.Buffer{}
	v.Magic = MagicBocchi
	v.Checksum = v.GenerateChecksum()

	err = binary.Write(buf, binary.BigEndian, *v)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (v *StripeHeaderFooter) UnmarshalBinary(data []byte) error {
	buf := bytes.NewBuffer(data)
	err := binary.Read(buf, binary.BigEndian, v)
	if err != nil {
		return err
	}
	if v.Magic != MagicBocchi {
		return errors.New("invalid magic")
	}
	if v.Checksum != v.GenerateChecksum() {
		return errors.New("invalid checksum")
	}
	return nil
}
