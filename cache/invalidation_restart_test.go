package cache

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func openInvalidationCache(t *testing.T, path string) *Cache {
	t.Helper()
	c, err := NewCache(1, []string{path}, []int64{32 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Init(); err != nil {
		t.Fatal(err)
	}
	return c
}

func assertInvalidationBody(t *testing.T, c *Cache, key, want string) {
	t.Helper()
	r, err := c.OpenReadCommitted([]byte(key))
	if err != nil {
		if want == "" {
			return
		}
		t.Fatal(err)
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if want == "" {
		if err == nil {
			t.Fatalf("purged object remained readable: %q", got)
		}
		return
	}
	if err != nil || string(got) != want {
		t.Fatalf("body = %q,%v; want %q", got, err, want)
	}
}

func seedInvalidationCache(t *testing.T, path string) {
	t.Helper()
	c := openInvalidationCache(t, path)
	for _, key := range []string{"https://a/folder/one", "https://a/folder/two", "https://a/keep"} {
		w, err := c.OpenWrite([]byte(key), 0)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte("fresh"))
		if err = w.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExactAndPrefixPurgeSurviveGracefulRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache")
	seedInvalidationCache(t, path)
	c := openInvalidationCache(t, path)
	assertInvalidationBody(t, c, "https://a/folder/one", "fresh")
	if keys, err := c.PurgeSelector([]byte("https://a/folder/one"), false); err != nil || len(keys) != 1 {
		t.Fatalf("recovered exact purge = %v,%v", keys, err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c = openInvalidationCache(t, path)
	assertInvalidationBody(t, c, "https://a/folder/one", "")
	assertInvalidationBody(t, c, "https://a/folder/two", "fresh")
	if keys, err := c.PurgeSelector([]byte("https://a/folder/"), true); err != nil || len(keys) != 1 {
		t.Fatalf("recovered prefix purge = %v,%v", keys, err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	c = openInvalidationCache(t, path)
	defer c.Close()
	assertInvalidationBody(t, c, "https://a/folder/two", "")
	assertInvalidationBody(t, c, "https://a/keep", "fresh")
}

func TestPurgeThenKilledProcessCannotRecoverOldCheckpoint(t *testing.T) {
	for _, mode := range []string{"exact", "prefix", "exact-save", "prefix-save"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cache")
			seedInvalidationCache(t, path)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(executable, "-test.run=^TestPurgeKilledProcessHelper$")
			cmd.Env = append(os.Environ(), "BAKEMONO_PURGE_CHILD="+mode, "BAKEMONO_PURGE_PATH="+path)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			if err = cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
			ready := make(chan bool, 1)
			go func() {
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					if scanner.Text() == "PURGED" {
						ready <- true
						return
					}
				}
				ready <- false
			}()
			select {
			case ok := <-ready:
				if !ok {
					_ = cmd.Wait()
					t.Fatalf("purge child failed before acknowledgement: %s", stderr.String())
				}
			case <-time.After(10 * time.Second):
				t.Fatal("purge child timed out")
			}
			if err = cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = cmd.Wait()
			c := openInvalidationCache(t, path)
			defer c.Close()
			assertInvalidationBody(t, c, "https://a/folder/one", "")
		})
	}
}

func TestPurgeKilledProcessHelper(t *testing.T) {
	mode := os.Getenv("BAKEMONO_PURGE_CHILD")
	if mode == "" {
		return
	}
	c := openInvalidationCache(t, os.Getenv("BAKEMONO_PURGE_PATH"))
	selector, prefix := "https://a/folder/one", false
	if mode == "prefix" || mode == "prefix-save" {
		selector, prefix = "https://a/folder/", true
	}
	if _, err := c.PurgeSelector([]byte(selector), prefix); err != nil {
		t.Fatal(err)
	}
	if mode == "exact-save" || mode == "prefix-save" {
		if err := c.SaveAll(); err != nil {
			t.Fatal(err)
		}
	}
	fmt.Println("PURGED")
	for {
		time.Sleep(time.Hour)
	}
}
