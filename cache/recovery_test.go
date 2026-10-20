package cache

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCorruptFragmentIsNeverReadable(t *testing.T) {
	c := safetyCache(t)
	key := []byte("corrupt-body")
	w, err := c.OpenWrite(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write([]byte("verified body")); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	s := c.KeyToStripe(key)
	_, _, d := s.DirMgr.Get(key)
	b := []byte{0}
	if _, err = s.Fd.ReadAt(b, int64(d.offset())+DocHeaderSize); err != nil {
		t.Fatal(err)
	}
	b[0] ^= 1
	if _, err = s.Fd.WriteAt(b, int64(d.offset())+DocHeaderSize); err != nil {
		t.Fatal(err)
	}
	s.RamCache.Delete(key)
	r, err := c.OpenReadCommitted(key)
	if err != nil {
		return
	}
	defer r.Close()
	if got, err := io.ReadAll(r); err == nil {
		t.Fatalf("corrupt object returned %q", got)
	}
}

func TestKilledSessionCannotRecoverHistoricalCheckpoint(t *testing.T) {
	if path := os.Getenv("BAKEMONO_CRASH_CACHE"); path != "" {
		c, err := NewCache(1, []string{path}, []int64{32 << 20})
		if err != nil {
			t.Fatal(err)
		}
		if err = c.Init(); err != nil {
			t.Fatal(err)
		}
		if err = c.Remove([]byte("crash-key")); err != nil && os.Getenv("BAKEMONO_CRASH_SAVE") != "alias" {
			t.Fatal(err)
		}
		if os.Getenv("BAKEMONO_CRASH_SAVE") == "1" {
			if err = c.SaveAll(); err != nil {
				t.Fatal(err)
			}
		}
		fmt.Println("ready-to-kill")
		select {}
	}
	for _, save := range []string{"0", "1", "alias"} {
		t.Run("save="+save, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cache")
			c, err := NewCache(1, []string{path}, []int64{32 << 20})
			if err != nil {
				t.Fatal(err)
			}
			if err = c.Init(); err != nil {
				t.Fatal(err)
			}
			w, err := c.OpenWrite([]byte("crash-key"), 0)
			if err != nil {
				t.Fatal(err)
			}
			w.Write([]byte("old checkpoint body"))
			if err = w.Close(); err != nil {
				t.Fatal(err)
			}
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
			childPath := path
			if save == "alias" {
				childPath = path + "-alias"
				if err = os.Link(path, childPath); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestKilledSessionCannotRecoverHistoricalCheckpoint$")
			cmd.Env = append(os.Environ(), "BAKEMONO_CRASH_CACHE="+childPath, "BAKEMONO_CRASH_SAVE="+save)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			ready := make(chan bool, 1)
			go func() {
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					if scanner.Text() == "ready-to-kill" {
						ready <- true
						return
					}
				}
				ready <- false
			}()
			select {
			case ok := <-ready:
				if !ok {
					t.Fatal("child exited before kill window")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("child never reached kill window")
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			c, err = NewCache(1, []string{path}, []int64{32 << 20})
			if err != nil {
				t.Fatal(err)
			}
			if err = c.Init(); err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			r, err := c.OpenReadCommitted([]byte("crash-key"))
			if err != nil {
				return
			}
			defer r.Close()
			if got, err := io.ReadAll(r); err == nil {
				t.Fatalf("killed session resurrected %q", got)
			}
		})
	}
}

func TestStripeOwnershipRejectsOverlapAndOtherProcess(t *testing.T) {
	if path := os.Getenv("BAKEMONO_OWNER_CACHE"); path != "" {
		s := NewStripe(path, 1<<20, 0)
		if err := s.Init(0); err != nil {
			t.Fatal(err)
		}
		if err := s.Open(); err == nil {
			s.Close()
			t.Fatal("second process opened owned inode")
		}
		return
	}
	path := filepath.Join(t.TempDir(), "cache")
	_, a := openStorageTest(t, path, 1<<20, 0, 0)
	alias := path + "-alias"
	if err := os.Link(path, alias); err != nil {
		t.Fatal(err)
	}
	overlap := NewStripe(alias, 1<<20, 512)
	if err := overlap.Init(0); err != nil {
		t.Fatal(err)
	}
	if err := overlap.Open(); err == nil {
		overlap.Close()
		t.Fatal("overlapping stripe acquired")
	}
	_, b := openStorageTest(t, alias, 1<<20, 1<<20, 0)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStripeOwnershipRejectsOverlapAndOtherProcess$")
	cmd.Env = append(os.Environ(), "BAKEMONO_OWNER_CACHE="+path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("inode lock lost while split stripe active: %v %s", err, out)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	_, again := openStorageTest(t, path, 1<<20, 0, 0)
	if err := again.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckpointAndDocumentCorruptionRecoverAsMiss(t *testing.T) {
	for _, kind := range []string{"valid", "body", "metadata", "total-length", "fragment-length", "header-length", "identity", "chain-length", "checkpoint-byte", "checkpoint-version", "directory-offset", "directory-tag", "directory-cycle", "free-cycle", "mapping-key", "mapping-length", "missing"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cache")
			c, s := openStorageTest(t, path, 8<<20, 0, 0)
			key := []byte("integrity-key")
			body := bytes.Repeat([]byte("z"), 2*TargetFragmentSize+17)
			w, err := c.OpenWrite(key, 0)
			if err != nil {
				t.Fatal(err)
			}
			info := NewCacheHTTPInfo()
			info.RequestURL = "https://example.test/object"
			w.SetHTTPInfo(info)
			if _, err = w.Write(body); err != nil {
				t.Fatal(err)
			}
			if err = w.Close(); err != nil {
				t.Fatal(err)
			}
			_, slot, d := s.DirMgr.Get(key)
			_, _, tail := s.DirMgr.Get(FragmentKey(key, 1))
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "valid":
			case "body", "metadata", "total-length", "fragment-length", "header-length", "identity", "chain-length":
				f, err := os.OpenFile(path, os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				off := int64(d.offset())
				if kind == "chain-length" {
					off = int64(tail.offset())
				}
				raw := make([]byte, DocHeaderSize)
				if _, err = f.ReadAt(raw, off); err != nil {
					t.Fatal(err)
				}
				var doc Doc
				if err = doc.UnmarshalBinary(raw); err != nil {
					t.Fatal(err)
				}
				raw = make([]byte, doc.Len)
				if _, err = f.ReadAt(raw, off); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "body":
					raw[len(raw)-1] ^= 1
				case "metadata":
					raw[DocHeaderSize+2] ^= 1
				case "total-length":
					raw[8] ^= 1
				case "fragment-length":
					binary.BigEndian.PutUint32(raw[4:], ^uint32(0))
				case "header-length":
					binary.BigEndian.PutUint32(raw[48:], ^uint32(0))
				case "identity":
					raw[32] ^= 1
				case "chain-length":
					doc.TotalLen++
					doc.Checksum = doc.CalculateChecksum(raw[DocHeaderSize:])
					encoded, _ := doc.MarshalBinary()
					copy(raw, encoded)
				}
				if _, err = f.WriteAt(raw, off); err != nil {
					t.Fatal(err)
				}
				if err = f.Close(); err != nil {
					t.Fatal(err)
				}
			default:
				checkpoint := s.checkpointPath()
				if kind == "missing" {
					if err = os.Remove(checkpoint); err != nil {
						t.Fatal(err)
					}
					break
				}
				raw, err := os.ReadFile(checkpoint)
				if err != nil {
					t.Fatal(err)
				}
				headerSize := binary.Size(checkpointHeader{})
				dirOff := headerSize + int(slot)*DirSize
				mapOff := headerSize + s.DirMgr.BinarySize()
				switch kind {
				case "checkpoint-byte":
					raw[0] ^= 1
				case "checkpoint-version":
					binary.BigEndian.PutUint32(raw[8:], 999)
				case "directory-offset":
					clear(raw[dirOff : dirOff+4])
					raw[dirOff] = 1
				case "directory-tag":
					raw[dirOff+4] ^= 1
				case "directory-cycle":
					binary.LittleEndian.PutUint16(raw[dirOff+6:], uint16(slot))
				case "free-cycle":
					free := s.DirMgr.DirFreeStart[0]
					binary.LittleEndian.PutUint16(raw[headerSize+int(free)*DirSize+6:], free)
				case "mapping-key":
					raw[mapOff+8] ^= 1
				case "mapping-length":
					binary.BigEndian.PutUint32(raw[mapOff:], ^uint32(0))
				}
				if kind != "checkpoint-byte" {
					clear(raw[headerSize-4 : headerSize])
					binary.BigEndian.PutUint32(raw[headerSize-4:], crc32.ChecksumIEEE(raw))
				}
				if err = os.WriteFile(checkpoint, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			c, _ = openStorageTest(t, path, 8<<20, 0, 0)
			got, err := readStorageObject(c, string(key))
			if kind == "valid" {
				if err != nil || !bytes.Equal(got, body) {
					t.Fatalf("valid graceful reopen: bytes=%d err=%v", len(got), err)
				}
			} else if err == nil {
				t.Fatalf("corrupt %s became a hit: bytes=%d", kind, len(got))
			}
		})
	}
}

type killedWriteDisk struct {
	*os.File
	start  int64
	window string
}

func (d *killedWriteDisk) WriteAt(p []byte, off int64) (int, error) {
	if off >= d.start && (d.window == "before-data" || d.window == "partial-data") {
		if d.window == "partial-data" {
			if _, err := d.File.WriteAt(p[:len(p)/2], off); err != nil {
				return 0, err
			}
		}
		fmt.Println("ready-to-kill")
		select {}
	}
	return d.File.WriteAt(p, off)
}
func (d *killedWriteDisk) Sync() error {
	if d.window == "before-clean-index" {
		if err := d.File.Sync(); err != nil {
			return err
		}
		fmt.Println("ready-to-kill")
		select {}
	}
	return d.File.Sync()
}

func TestKilledWriteAndCheckpointWindowsRecoverOnlyVerifiedObjects(t *testing.T) {
	if path := os.Getenv("BAKEMONO_WINDOW_CACHE"); path != "" {
		c, err := NewCache(1, []string{path}, []int64{8 << 20})
		if err != nil {
			t.Fatal(err)
		}
		if err = c.Init(); err != nil {
			t.Fatal(err)
		}
		s := c.Stripes[0]
		window := os.Getenv("BAKEMONO_WINDOW")
		s.disk = &killedWriteDisk{File: s.Fd, start: s.Start, window: window}
		w, err := c.OpenWrite([]byte("window-key"), 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = w.Write([]byte("new complete body")); err != nil {
			t.Fatal(err)
		}
		if err = w.Close(); err != nil {
			t.Fatal(err)
		}
		if window == "before-clean-index" || window == "clean-close" {
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
		}
		fmt.Println("ready-to-kill")
		select {}
	}
	for _, window := range []string{"before-data", "partial-data", "directory-published", "before-clean-index", "clean-close"} {
		t.Run(window, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cache")
			c, _ := openStorageTest(t, path, 8<<20, 0, 0)
			writeStorageObject(t, c, "window-key", []byte("old historical body"))
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestKilledWriteAndCheckpointWindowsRecoverOnlyVerifiedObjects$")
			cmd.Env = append(os.Environ(), "BAKEMONO_WINDOW_CACHE="+path, "BAKEMONO_WINDOW="+window)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stderr = os.Stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer cmd.Process.Kill()
			ready := make(chan bool, 1)
			go func() {
				scan := bufio.NewScanner(stdout)
				for scan.Scan() {
					if scan.Text() == "ready-to-kill" {
						ready <- true
						return
					}
				}
				ready <- false
			}()
			select {
			case ok := <-ready:
				if !ok {
					t.Fatal("child failed before kill window")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("kill window timeout")
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			c, _ = openStorageTest(t, path, 8<<20, 0, 0)
			got, err := readStorageObject(c, "window-key")
			if window == "clean-close" {
				if err != nil || string(got) != "new complete body" {
					t.Fatalf("clean checkpoint lost: %q %v", got, err)
				}
			} else if err == nil {
				t.Fatalf("unclean %s became hit %q", window, got)
			}
		})
	}
}
