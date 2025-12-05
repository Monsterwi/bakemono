package bakemono

import (
	"bytes"
	"encoding/binary"
)

// HTTPInfo represents the metadata for a cached object, including headers and fragment info.
// Aligned with ATS HTTPInfo concept.
type HTTPInfo struct {
	PayloadSize  uint64
	FragmentSize uint32
	EarliestKey  [16]byte

	// In a real implementation, these would be parsed headers.
	// For now, we store raw bytes or empty.
	RequestHeaders  []byte
	ResponseHeaders []byte

	// TODO: Fragment Table for random access optimization?
	// ATS stores offsets in FragOffset* frag_offsets
}

// MarshalBinary serializes HTTPInfo.
// Format:
// [PayloadSize:8][FragmentSize:4][EarliestKey:16][ReqLen:4][ReqBytes][RespLen:4][RespBytes]
func (h *HTTPInfo) MarshalBinary() ([]byte, error) {
	buf := new(bytes.Buffer)

	if err := binary.Write(buf, binary.BigEndian, h.PayloadSize); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, h.FragmentSize); err != nil {
		return nil, err
	}
	if _, err := buf.Write(h.EarliestKey[:]); err != nil {
		return nil, err
	}

	reqLen := uint32(len(h.RequestHeaders))
	if err := binary.Write(buf, binary.BigEndian, reqLen); err != nil {
		return nil, err
	}
	if reqLen > 0 {
		if _, err := buf.Write(h.RequestHeaders); err != nil {
			return nil, err
		}
	}

	respLen := uint32(len(h.ResponseHeaders))
	if err := binary.Write(buf, binary.BigEndian, respLen); err != nil {
		return nil, err
	}
	if respLen > 0 {
		if _, err := buf.Write(h.ResponseHeaders); err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}

// UnmarshalBinary deserializes HTTPInfo.
func (h *HTTPInfo) UnmarshalBinary(data []byte) error {
	buf := bytes.NewBuffer(data)

	if err := binary.Read(buf, binary.BigEndian, &h.PayloadSize); err != nil {
		return err
	}
	if err := binary.Read(buf, binary.BigEndian, &h.FragmentSize); err != nil {
		return err
	}
	if _, err := buf.Read(h.EarliestKey[:]); err != nil {
		return err
	}

	var reqLen uint32
	if err := binary.Read(buf, binary.BigEndian, &reqLen); err != nil {
		return err
	}
	if reqLen > 0 {
		h.RequestHeaders = make([]byte, reqLen)
		if _, err := buf.Read(h.RequestHeaders); err != nil {
			return err
		}
	}

	var respLen uint32
	if err := binary.Read(buf, binary.BigEndian, &respLen); err != nil {
		return err
	}
	if respLen > 0 {
		h.ResponseHeaders = make([]byte, respLen)
		if _, err := buf.Read(h.ResponseHeaders); err != nil {
			return err
		}
	}

	return nil
}

// Vector stores multiple Alternates (HTTPInfo).
// ATS stores this in the First Doc.
type Vector struct {
	Alternates []HTTPInfo
}

// MarshalBinary serializes Vector.
// Format: [Count:4][Alt1Size:4][Alt1Bytes][Alt2Size:4][Alt2Bytes]...
func (v *Vector) MarshalBinary() ([]byte, error) {
	buf := new(bytes.Buffer)
	count := uint32(len(v.Alternates))

	if err := binary.Write(buf, binary.BigEndian, count); err != nil {
		return nil, err
	}

	for _, alt := range v.Alternates {
		altBytes, err := alt.MarshalBinary()
		if err != nil {
			return nil, err
		}
		size := uint32(len(altBytes))
		if err := binary.Write(buf, binary.BigEndian, size); err != nil {
			return nil, err
		}
		if _, err := buf.Write(altBytes); err != nil {
			return nil, err
		}
	}

	return buf.Bytes(), nil
}

// UnmarshalBinary deserializes Vector.
func (v *Vector) UnmarshalBinary(data []byte) error {
	buf := bytes.NewBuffer(data)
	var count uint32
	if err := binary.Read(buf, binary.BigEndian, &count); err != nil {
		return err
	}

	v.Alternates = make([]HTTPInfo, count)
	for i := uint32(0); i < count; i++ {
		var size uint32
		if err := binary.Read(buf, binary.BigEndian, &size); err != nil {
			return err
		}
		altData := make([]byte, size)
		if _, err := buf.Read(altData); err != nil {
			return err
		}
		if err := v.Alternates[i].UnmarshalBinary(altData); err != nil {
			return err
		}
	}

	return nil
}
