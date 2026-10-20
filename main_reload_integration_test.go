package main_test

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestBinarySIGHUPReload(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "bakemono")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	hold := make(chan struct{})
	release := sync.OnceFunc(func() { close(hold) })
	oldOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.URL.Path == "/held" {
			io.WriteString(w, "old-")
			http.NewResponseController(w).Flush()
			select {
			case <-hold:
			case <-r.Context().Done():
				return
			}
			io.WriteString(w, "complete")
			return
		}
		io.WriteString(w, "old")
	}))
	defer func() { release(); oldOrigin.Close() }()
	newOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		io.WriteString(w, "new")
	}))
	defer newOrigin.Close()
	dir := t.TempDir()
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserved.Addr().(*net.TCPAddr).Port
	reserved.Close()
	records := func(origin string) string {
		return fmt.Sprintf("CONFIG proxy.config.http.server_ports INT %d\nCONFIG proxy.config.log.logfile_dir STRING %s\nCONFIG proxy.config.http.parent_proxy.upstream_algo STRING round-robin\nCONFIG proxy.config.http.parent_proxy.upstream_targets STRING %s\nCONFIG proxy.config.http.shutdown_timeout INT 2\n", port, dir, origin)
	}
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("records.config", records(oldOrigin.URL))
	storage := filepath.Join(dir, "cache.db") + " 32M\n"
	write("storage.config", storage)
	output, err := os.Create(filepath.Join(dir, "process.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	cmd := exec.Command(binary, "-c", dir)
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			cmd.Process.Kill()
			<-done
		}
		if t.Failed() {
			logs, _ := os.ReadFile(output.Name())
			t.Log(string(logs))
		}
	})
	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	body := func(path string) string {
		resp, err := client.Get(base + path)
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return ""
		}
		return string(data)
	}
	waitFor(t, "initial origin", func() bool { return body("/object") == "old" })
	held, err := client.Get(base + "/held")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Body.Close()
	prefix := make([]byte, 4)
	if _, err := io.ReadFull(held.Body, prefix); err != nil {
		t.Fatal(err)
	}
	write("records.config", records(newOrigin.URL))
	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "new origin after SIGHUP", func() bool { return body("/object") == "new" })
	// A second transition is rejected until the old response's body lease drains.
	write("records.config", records(oldOrigin.URL))
	cmd.Process.Signal(syscall.SIGHUP)
	waitFor(t, "busy reload report", func() bool {
		logs, _ := os.ReadFile(output.Name())
		return strings.Contains(string(logs), "reload busy")
	})
	if got := body("/object"); got != "new" {
		t.Fatalf("busy reload changed origin: %q", got)
	}
	release()
	suffix, err := io.ReadAll(held.Body)
	held.Body.Close()
	if err != nil || string(prefix)+string(suffix) != "old-complete" {
		t.Fatalf("reload truncated original body: %q %v", suffix, err)
	}
	// Malformed records and in-place storage changes cannot mutate the running service.
	write("records.config", records(oldOrigin.URL)+"CONFIG proxy.config.http.max_requests INT broken\n")
	cmd.Process.Signal(syscall.SIGHUP)
	waitFor(t, "invalid reload report", func() bool {
		logs, _ := os.ReadFile(output.Name())
		return strings.Contains(string(logs), "invalid integer")
	})
	if got := body("/object"); got != "new" {
		t.Fatalf("invalid reload changed origin: %q", got)
	}
	write("records.config", records(oldOrigin.URL))
	write("storage.config", filepath.Join(dir, "cache.db")+" 64M\n")
	cmd.Process.Signal(syscall.SIGHUP)
	waitFor(t, "storage restart requirement", func() bool {
		logs, _ := os.ReadFile(output.Name())
		return strings.Contains(string(logs), "contents changed; restart required")
	})
	if got := body("/object"); got != "new" {
		t.Fatalf("storage reload changed origin: %q", got)
	}
	write("storage.config", storage)
	cmd.Process.Signal(syscall.SIGHUP)
	waitFor(t, "successful reload after drain", func() bool { return body("/object") == "old" })
	cmd.Process.Signal(syscall.SIGTERM)
	select {
	case err := <-done:
		stopped = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown did not join runtimes")
	}
}
