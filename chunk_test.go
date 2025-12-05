package bakemono

import (
	"bytes"
	"crypto/md5"
	"encoding/binary"
	"os"
	"reflect"
	"testing"
)

func TestChunk_SetVerityGet(t *testing.T) {
	chunk := &Chunk{}
	err := chunk.Set([]byte("key"), []byte("value"))
	if err != nil {
		t.Fatal(err)
	}

	err = chunk.Verify()
	if err != nil {
		t.Fatal(err)
	}

	keyHash, data := chunk.GetKeyData()
	expectedHash := md5.Sum([]byte("key"))
	if !bytes.Equal(keyHash, expectedHash[:]) {
		t.Fatal("key hash is not equal to expected hash")
	}
	if string(data) != "value" {
		t.Fatal("data is not equal to \"value\"")
	}
}

func TestChunk_BadSet(t *testing.T) {
	chunk := &Chunk{}
	badKey := make([]byte, ChunkKeyMaxSize+1)
	err := chunk.Set(badKey, []byte("value"))
	if err == nil {
		t.Fatal(err)
	}
	badData := make([]byte, ChunkDataSize+1)
	err = chunk.Set([]byte("key"), badData)
	if err == nil {
		t.Fatal(err)
	}
	err = chunk.Set(badKey, badData)
	if err == nil {
		t.Fatal(err)
	}
}

func TestChunk_Marshal_Unmarshal_Binary(t *testing.T) {
	chunk := &Chunk{}
	err := chunk.Set([]byte("key"), []byte("value114514"))
	if err != nil {
		t.Fatal(err)
	}
	bin, err := chunk.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	chunk2 := &Chunk{}
	err = chunk2.UnmarshalBinary(bin)
	if err != nil {
		t.Fatal(err)
	}
	err = chunk2.Verify()
	if err != nil {
		t.Fatal(err)
	}
	equal := reflect.DeepEqual(chunk, chunk2)
	if !equal {
		t.Fatal("chunk2 is not equal to chunk1")
	}

	k, v := chunk2.GetKeyData()
	expectedHash := md5.Sum([]byte("key"))
	if !bytes.Equal(k, expectedHash[:]) {
		t.Fatal("key hash is not equal to expected hash")
	}
	if string(v) != "value114514" {
		t.Fatal("data is not equal to \"value114514\"")
	}
}

func TestChunk_BadRead(t *testing.T) {
	chunk := &Chunk{}
	err := chunk.Set([]byte("key"), []byte("value114514"))
	if err != nil {
		t.Fatal(err)
	}
	bin, err := chunk.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	// bad body length
	chunk2 := &Chunk{}
	err = chunk2.UnmarshalBinary(bin[:len(bin)-1])
	if err == nil {
		t.Fatal("chunk2.UnmarshalBinary should return an error")
	}

	// bad body content
	chunk3 := &Chunk{}
	bin2 := make([]byte, len(bin))
	copy(bin2, bin)
	bin2[0] = bin2[0] + 1
	err = chunk3.UnmarshalBinary(bin2)
	if err == nil {
		t.Fatal("chunk3.UnmarshalBinary should return an error")
	}
}

func TestChunk_WriteAt(t *testing.T) {
	chunk := &Chunk{}
	err := chunk.Set([]byte("key"), []byte("value"))
	if err != nil {
		t.Fatal(err)
	}
	// open file for writing
	path := os.TempDir() + "/test_chunk_writeat"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0666)
	if err != nil {
		t.Fatal(err)
	}
	defer func(f *os.File) {
		err := f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}(f)
	defer func(name string) {
		err := os.Remove(name)
		if err != nil {
			t.Fatal(err)
		}
	}(path)
	err = chunk.WriteAt(f, 0)
	if err != nil {
		t.Fatal(err)
	}
}

func TestDoc_Marshal_UnmarshalBinary(t *testing.T) {
	// create a Doc struct
	// marshal it to binary
	// check if the binary is equal to the expected binary
	// if not, then the test fails
	ch := Doc{
		Magic:    MagicChunk,
		Checksum: 0xb0cc1000,
		KeyHash:  [16]byte{0x12, 0x34, 0x56, 0x78, 0x9a, 0xbc, 0xde, 0xf0, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88},
		Len:      0x11451419 + uint32(binary.Size(Doc{})),
		TotalLen: 0x11451419,
	}
	b, err := ch.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(b))
	ch2 := Doc{}
	err = ch2.UnmarshalBinary(b)
	if err != nil {
		t.Fatal(err)
	}
	equal := reflect.DeepEqual(ch, ch2)
	if !equal {
		t.Fatal("Doc2 is not equal to Doc1")
	}
	t.Log("Doc2 is equal to Doc1")
}
