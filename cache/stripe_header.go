package cache

import (
	"bytes"
	"encoding/binary"
)

const (
	StripeHeaderMagic = 0xBADBABE
	StripeHeaderVer   = 1
	StripeHeaderSize  = 24 // 4+4+8+4+4
)

type StripeHeader struct {
	Magic      uint32
	Version    uint32
	WritePos   int64
	Phase      bool
	SyncSerial uint32
}

func (h *StripeHeader) MarshalBinary() ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := binary.Write(buf, binary.BigEndian, h.Magic); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, h.Version); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, h.WritePos); err != nil {
		return nil, err
	}
	// Bool as uint8
	var phase uint8
	if h.Phase {
		phase = 1
	}
	if err := binary.Write(buf, binary.BigEndian, phase); err != nil {
		return nil, err
	}
	// Pad to align or just space
	if err := binary.Write(buf, binary.BigEndian, uint8(0)); err != nil { // pad 1
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, uint16(0)); err != nil { // pad 2
		return nil, err
	}

	if err := binary.Write(buf, binary.BigEndian, h.SyncSerial); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (h *StripeHeader) UnmarshalBinary(data []byte) error {
	buf := bytes.NewReader(data)
	if err := binary.Read(buf, binary.BigEndian, &h.Magic); err != nil {
		return err
	}
	if err := binary.Read(buf, binary.BigEndian, &h.Version); err != nil {
		return err
	}
	if err := binary.Read(buf, binary.BigEndian, &h.WritePos); err != nil {
		return err
	}
	var phase uint8
	if err := binary.Read(buf, binary.BigEndian, &phase); err != nil {
		return err
	}
	h.Phase = phase == 1

	// Read pads
	var pad8 uint8
	binary.Read(buf, binary.BigEndian, &pad8)
	var pad16 uint16
	binary.Read(buf, binary.BigEndian, &pad16)

	if err := binary.Read(buf, binary.BigEndian, &h.SyncSerial); err != nil {
		return err
	}
	return nil
}
