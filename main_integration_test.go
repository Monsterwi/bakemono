package main_test

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// TestBinaryCacheMissThenHit runs the shipped entrypoint with only temporary
// storage and a loopback origin. The access log marks handler completion, so the
// second request cannot race the first request's cache commit.
func TestBinaryCacheMissThenHit(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "bakemono")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build local binary: %v\n%s", err, output)
	}
	for _, force := range []bool{false, true} {
		t.Run(fmt.Sprintf("grace-expiry=%t", force), func(t *testing.T) { binaryLifecycle(t, binary, force) })
	}
}

func binaryLifecycle(t *testing.T, binary string, force bool) {
	dir := t.TempDir()
	body := bytes.Repeat([]byte("cache integration payload\n"), 4096)
	finishFill := make(chan struct{})
	fillEntered := make(chan struct{})
	var originCalls atomic.Int64
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originCalls.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.URL.Path == "/draining-fill" {
			close(fillEntered)
			w.Write(body[:1])
			w.(http.Flusher).Flush()
			select {
			case <-finishFill:
			case <-r.Context().Done():
				return
			}
			w.Write(body[1:])
			return
		}
		_, _ = w.Write(body)
	}))
	defer origin.Close()

	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	if err := reservation.Close(); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`CONFIG proxy.config.http.server_ports INT %d
CONFIG proxy.config.log.logfile_dir STRING %s
CONFIG proxy.config.log.access_log_filename STRING access.log
CONFIG proxy.config.log.error_log_filename STRING error.log
CONFIG proxy.config.http.parent_proxy.upstream_algo STRING round-robin
CONFIG proxy.config.http.parent_proxy.upstream_targets STRING %s
CONFIG proxy.config.http.keep_alive_no_activity_timeout_in INT 5
CONFIG proxy.config.http.transaction_no_activity_timeout_in INT 10
CONFIG proxy.config.http.transaction_no_activity_timeout_out INT 10
CONFIG proxy.config.http.shutdown_timeout INT 1
`, port, dir, origin.URL)
	for name, data := range map[string]string{
		"records.config": config,
		"storage.config": filepath.Join(dir, "cache.db") + " 32M\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
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
			_ = cmd.Process.Kill()
			<-done
		}
		if t.Failed() {
			logs, _ := os.ReadFile(output.Name())
			t.Logf("binary output:\n%s", logs)
		}
	})
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	waitFor(t, "listener ready", func() bool {
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	})
	if err := cmd.Process.Signal(syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "local resource snapshot", func() bool {
		logs, _ := os.ReadFile(output.Name())
		return regexp.MustCompile(`HTTP resource stats: .*Goroutines:[1-9][0-9]*`).Match(logs)
	})
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	for _, status := range []string{"MISS", "HIT"} {
		resp, err := client.Get("http://" + address + "/baseline-object")
		if err != nil {
			t.Fatal(err)
		}
		got, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil || resp.StatusCode != http.StatusOK || !bytes.Equal(got, body) {
			t.Fatalf("%s response: status=%d bytes=%d read=%v; want 200 and %d exact bytes", status, resp.StatusCode, len(got), readErr, len(body))
		}
		waitFor(t, status+" completed", func() bool {
			logs, err := os.ReadFile(filepath.Join(dir, "access.log"))
			return err == nil && strings.Contains(string(logs), `"cache_status":"`+status+`"`)
		})
		if calls := originCalls.Load(); calls != 1 {
			t.Fatalf("after %s: origin calls=%d, want 1", status, calls)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "cache.db")); err != nil || info.Size() == 0 {
		t.Fatalf("temporary disk cache was not created: %v", err)
	}

	type downloadResult struct {
		body   []byte
		err    error
		status int
	}
	download := make(chan downloadResult, 1)
	go func() {
		resp, err := client.Get("http://" + address + "/draining-fill")
		if err != nil {
			download <- downloadResult{err: err}
			return
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		download <- downloadResult{body, err, resp.StatusCode}
	}()
	select {
	case <-fillEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("origin fill did not start")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "listener stops intake", func() bool {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if conn != nil {
			conn.Close()
		}
		return err != nil
	})
	if !force {
		close(finishFill)
	}

	result := <-download
	if force {
		if result.err == nil && result.status == http.StatusOK {
			t.Fatal("expired grace produced complete response")
		}
	} else if result.err != nil || result.status != http.StatusOK || !bytes.Equal(result.body, body) {
		t.Fatalf("graceful fill: status=%d bytes=%d error=%v", result.status, len(result.body), result.err)
	}
	select {
	case err := <-done:
		stopped = true
		if force && err == nil {
			t.Fatal("expired grace returned successful process exit")
		}
		if !force && err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("binary did not stop after SIGTERM")
	}
}

func waitFor(t *testing.T, description string, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", description)
		case <-tick.C:
		}
	}
}
