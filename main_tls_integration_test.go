package main_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestBinaryTLSAndHTTP2(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "razor")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}
	hold := make(chan struct{})
	var unhealthy atomic.Bool
	var probes atomic.Int64
	origin := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" && r.URL.Path == "/ready" {
			probes.Add(1)
			if unhealthy.Load() {
				w.WriteHeader(503)
			} else {
				w.WriteHeader(200)
			}
			return
		}
		w.Header().Set("Seen-Origin-Proto", strconv.Itoa(r.ProtoMajor))
		if r.URL.Path == "/hold" {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Length", "2")
			w.Write([]byte("a"))
			http.NewResponseController(w).Flush()
			select {
			case <-hold:
				w.Write([]byte("b"))
			case <-r.Context().Done():
			}
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Seen-Proto", r.Header.Get("X-Forwarded-Proto"))
		io.WriteString(w, "verified origin")
	}))
	origin.EnableHTTP2 = true
	origin.StartTLS()
	defer origin.Close()
	certDir := t.TempDir()
	certFile, keyFile := filepath.Join(certDir, "cert.pem"), filepath.Join(certDir, "key.pem")
	key, err := x509.MarshalPKCS8PrivateKey(origin.TLS.Certificates[0].PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	for path, block := range map[string]*pem.Block{certFile: {Type: "CERTIFICATE", Bytes: origin.Certificate().Raw}, keyFile: {Type: "PRIVATE KEY", Bytes: key}} {
		if err := os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
			t.Fatal(err)
		}
	}
	roots := x509.NewCertPool()
	roots.AddCert(origin.Certificate())
	settings := map[string]string{"ssl.cert_file": certFile, "ssl.key_file": keyFile, "http.upstream_ca_file": certFile, "http.trusted_proxies": "127.0.0.0/8"}
	address, stop := startNetworkBinary(t, binary, origin.URL, settings)
	defer stop()
	for _, h2 := range []bool{true, false} {
		tr := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: roots}, ForceAttemptHTTP2: h2}
		if !h2 {
			tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
		}
		client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
		request, _ := http.NewRequest("GET", "https://"+address+"/protocol", nil)
		request.Header.Set("X-Forwarded-Proto", "http")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		client.CloseIdleConnections()
		want := 1
		if h2 {
			want = 2
		}
		if err != nil || response.ProtoMajor != want || response.StatusCode != 200 || string(body) != "verified origin" || response.Header.Get("Seen-Proto") != "http" || response.Header.Get("Seen-Origin-Proto") != "2" {
			t.Fatalf("HTTP/%d: proto=%s status=%d body=%q scheme=%q err=%v", want, response.Proto, response.StatusCode, body, response.Header.Get("Seen-Proto"), err)
		}
		if h2 && response.TLS.NegotiatedProtocol != "h2" {
			t.Fatalf("ALPN = %q", response.TLS.NegotiatedProtocol)
		}
	}
	stop()
	t.Run("origin-untrusted", func(t *testing.T) {
		address, stop := startNetworkBinary(t, binary, origin.URL, map[string]string{})
		defer stop()
		client := &http.Client{Timeout: 5 * time.Second}
		response, err := client.Get("http://" + address + "/untrusted")
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, response.Body)
		response.Body.Close()
		if response.StatusCode != 502 {
			t.Fatalf("untrusted origin status=%d", response.StatusCode)
		}
	})
	t.Run("invalid-certificate-fails-before-listen", func(t *testing.T) {
		startNetworkBinary(t, binary, origin.URL, map[string]string{"ssl.cert_file": filepath.Join(t.TempDir(), "missing.pem"), "ssl.key_file": keyFile}, true)
	})
	for _, budget := range []string{"request", "origin"} {
		t.Run("h2-"+budget+"-budget", func(t *testing.T) {
			settings := map[string]string{"ssl.cert_file": certFile, "ssl.key_file": keyFile, "http.upstream_ca_file": certFile, "http.max_queued_requests": "0", "http.max_origin_queued_requests": "0"}
			if budget == "request" {
				settings["http.max_requests"] = "1"
			} else {
				settings["http.max_origin_requests"] = "1"
			}
			address, stop := startNetworkBinary(t, binary, origin.URL, settings)
			defer stop()
			client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}, ForceAttemptHTTP2: true, MaxConnsPerHost: 1}}
			defer client.CloseIdleConnections()
			direct, _ := http.NewRequest("GET", "https://"+address+"/direct", nil)
			direct.Header.Set("X-Forwarded-Proto", "http")
			verified, err := client.Do(direct)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, verified.Body)
			verified.Body.Close()
			if verified.Header.Get("Seen-Proto") != "https" {
				t.Fatal("untrusted forwarded scheme overrode actual TLS")
			}
			active, err := client.Get("https://" + address + "/hold")
			if err != nil {
				t.Fatal(err)
			}
			defer active.Body.Close()
			response, err := client.Get("https://" + address + "/overflow")
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if active.ProtoMajor != 2 || response.ProtoMajor != 2 || response.StatusCode != 503 {
				t.Fatalf("active=%s next=%s status=%d", active.Proto, response.Proto, response.StatusCode)
			}
			active.Body.Close()
		})
	}
	t.Run("health-removes-and-restores", func(t *testing.T) {
		address, stop := startNetworkBinary(t, binary, origin.URL, map[string]string{"http.upstream_ca_file": certFile, "http.health.interval": "1", "http.health.timeout": "1", "http.health.failures": "1", "http.health.successes": "1", "http.health.path": "/ready"})
		defer stop()
		client := &http.Client{Timeout: 3 * time.Second}
		status := func() int {
			response, err := client.Get("http://" + address + "/health-object")
			if err != nil {
				return 0
			}
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			return response.StatusCode
		}
		waitFor(t, "healthy HTTPS probe", func() bool { return probes.Load() > 0 && status() == 200 })
		unhealthy.Store(true)
		waitFor(t, "unhealthy origin removed", func() bool { return status() == 502 })
		unhealthy.Store(false)
		waitFor(t, "recovered origin rejoined", func() bool { return status() == 200 })
		stop()
		count := probes.Load()
		time.Sleep(1100 * time.Millisecond)
		if probes.Load() != count {
			t.Fatal("binary health checks survived shutdown")
		}
	})
}

// Each process owns only this test's small temporary cache and configuration.
func startNetworkBinary(t *testing.T, binary, origin string, settings map[string]string, wantFailure ...bool) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	port := binaryPort(t)
	records := fmt.Sprintf("CONFIG proxy.config.http.server_ports INT %d\nCONFIG proxy.config.log.logfile_dir STRING %s\nCONFIG proxy.config.http.parent_proxy.upstream_algo STRING round-robin\nCONFIG proxy.config.http.parent_proxy.upstream_targets STRING %s\nCONFIG proxy.config.http.shutdown_timeout INT 2\n", port, dir, origin)
	for key, value := range settings {
		records += "CONFIG proxy.config." + key + " STRING " + value + "\n"
	}
	for name, data := range map[string]string{"records.config": records, "storage.config": filepath.Join(dir, "cache.db") + " 32M\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	output, err := os.Create(filepath.Join(dir, "process.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { output.Close() })
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
			t.Logf("binary output:\n%s", logs)
		}
	})
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if len(wantFailure) > 0 && wantFailure[0] {
		select {
		case err := <-done:
			stopped = true
			if err == nil {
				t.Fatal("invalid TLS configuration started")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("invalid TLS configuration did not fail startup")
		}
		conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if conn != nil {
			conn.Close()
		}
		if err == nil {
			t.Fatal("failed TLS startup left plaintext listener")
		}
		return address, func() {}
	}
	waitFor(t, "network listener", func() bool {
		conn, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if conn != nil {
			conn.Close()
		}
		return err == nil
	})
	return address, func() {
		if stopped {
			return
		}
		cmd.Process.Signal(syscall.SIGTERM)
		select {
		case err := <-done:
			stopped = true
			if err != nil {
				t.Errorf("network shutdown: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("network binary did not stop")
		}
	}
}
