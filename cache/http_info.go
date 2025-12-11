package cache

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/http"
	"time"
)

// CacheHTTPInfo represents the metadata associated with a cached HTTP object.
// It mirrors ATS HTTPInfo.
type CacheHTTPInfo struct {
	RequestTime  time.Time
	ResponseTime time.Time

	// Simplified headers
	RequestMethod string
	RequestURL    string
	Status        int

	// We can store raw headers or a map
	ResponseHeaders http.Header
}

func NewCacheHTTPInfo() *CacheHTTPInfo {
	return &CacheHTTPInfo{
		ResponseHeaders: make(http.Header),
	}
}

// MarshalBinary serializes the HTTP info.
// Format:
// [RequestTime(8)][ResponseTime(8)][MethodLen(2)][Method][URLLen(2)][URL][Status(2)][HeadersLen(4)][HeadersJSON/Gob]
func (h *CacheHTTPInfo) MarshalBinary() ([]byte, error) {
	buf := new(bytes.Buffer)

	// Timestamps
	if err := binary.Write(buf, binary.BigEndian, h.RequestTime.UnixNano()); err != nil {
		return nil, err
	}
	if err := binary.Write(buf, binary.BigEndian, h.ResponseTime.UnixNano()); err != nil {
		return nil, err
	}

	// Method
	methodBytes := []byte(h.RequestMethod)
	if len(methodBytes) > 65535 {
		return nil, fmt.Errorf("method too long")
	}
	if err := binary.Write(buf, binary.BigEndian, uint16(len(methodBytes))); err != nil {
		return nil, err
	}
	buf.Write(methodBytes)

	// URL
	urlBytes := []byte(h.RequestURL)
	if len(urlBytes) > 65535 {
		return nil, fmt.Errorf("url too long")
	}
	if err := binary.Write(buf, binary.BigEndian, uint16(len(urlBytes))); err != nil {
		return nil, err
	}
	buf.Write(urlBytes)

	// Status
	if err := binary.Write(buf, binary.BigEndian, uint16(h.Status)); err != nil {
		return nil, err
	}

	// Headers
	// For simplicity, verify if we should use a better format.
	// Simple Key-Value pairs: [Count(2)] [KeyLen(2)][Key][ValLen(2)][Value]...
	// http.Header is map[string][]string

	headerBuf := new(bytes.Buffer)
	count := 0
	for k, vals := range h.ResponseHeaders {
		for _, v := range vals {
			count++
			// Key
			kBytes := []byte(k)
			binary.Write(headerBuf, binary.BigEndian, uint16(len(kBytes)))
			headerBuf.Write(kBytes)
			// Value
			vBytes := []byte(v)
			binary.Write(headerBuf, binary.BigEndian, uint16(len(vBytes)))
			headerBuf.Write(vBytes)
		}
	}

	if err := binary.Write(buf, binary.BigEndian, uint16(count)); err != nil {
		return nil, err
	}
	buf.Write(headerBuf.Bytes())

	return buf.Bytes(), nil
}

func (h *CacheHTTPInfo) UnmarshalBinary(data []byte) error {
	buf := bytes.NewReader(data)

	// Timestamps
	var reqTime, respTime int64
	if err := binary.Read(buf, binary.BigEndian, &reqTime); err != nil {
		return err
	}
	h.RequestTime = time.Unix(0, reqTime)

	if err := binary.Read(buf, binary.BigEndian, &respTime); err != nil {
		return err
	}
	h.ResponseTime = time.Unix(0, respTime)

	// Method
	var methodLen uint16
	if err := binary.Read(buf, binary.BigEndian, &methodLen); err != nil {
		return err
	}
	methodBytes := make([]byte, methodLen)
	if _, err := buf.Read(methodBytes); err != nil {
		return err
	}
	h.RequestMethod = string(methodBytes)

	// URL
	var urlLen uint16
	if err := binary.Read(buf, binary.BigEndian, &urlLen); err != nil {
		return err
	}
	urlBytes := make([]byte, urlLen)
	if _, err := buf.Read(urlBytes); err != nil {
		return err
	}
	h.RequestURL = string(urlBytes)

	// Status
	var status uint16
	if err := binary.Read(buf, binary.BigEndian, &status); err != nil {
		return err
	}
	h.Status = int(status)

	// Headers
	h.ResponseHeaders = make(http.Header)
	var count uint16
	if err := binary.Read(buf, binary.BigEndian, &count); err != nil {
		return err
	}

	for i := 0; i < int(count); i++ {
		var kLen uint16
		if err := binary.Read(buf, binary.BigEndian, &kLen); err != nil {
			return err
		}
		kBytes := make([]byte, kLen)
		if _, err := buf.Read(kBytes); err != nil {
			return err
		}

		var vLen uint16
		if err := binary.Read(buf, binary.BigEndian, &vLen); err != nil {
			return err
		}
		vBytes := make([]byte, vLen)
		if _, err := buf.Read(vBytes); err != nil {
			return err
		}

		h.ResponseHeaders.Add(string(kBytes), string(vBytes))
	}

	return nil
}
