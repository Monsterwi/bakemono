package main_test

import (
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestBinaryPortReservationAvoidsEphemeralAndWildcardConflict(t *testing.T) {
	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data))
	low, _ := strconv.Atoi(fields[0])
	high, _ := strconv.Atoi(fields[1])
	port := binaryPort(t)
	if port < 1024 || (port >= low && port <= high) {
		t.Fatalf("port %d is privileged or in ephemeral range %d-%d", port, low, high)
	}
	occupied, err := net.Listen("tcp4", "127.0.0.2:"+strconv.Itoa(port))
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	listener, err := reserveBinaryListener(low, high, port)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	selected := listener.Addr().(*net.TCPAddr).Port
	if selected == port || selected < 1024 || (selected >= low && selected <= high) {
		t.Fatalf("selected occupied/ephemeral port %d (occupied=%d, ephemeral=%d-%d)", selected, port, low, high)
	}
}

// Released before spawn; staying outside the outbound pool prevents automatic
// origin/health connections from taking the binary's port during startup/restart.
func binaryPort(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		t.Fatalf("invalid kernel ephemeral port range: %q", data)
	}
	low, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatal(err)
	}
	high, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatal(err)
	}
	listener, err := reserveBinaryListener(low, high, 1024+rand.IntN(65536-1024))
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func reserveBinaryListener(low, high, start int) (net.Listener, error) {
	for offset, tried := 0, 0; offset < 65536-1024 && tried < 128; offset++ {
		port := 1024 + (start-1024+offset)%(65536-1024)
		if port >= low && port <= high {
			continue
		}
		tried++
		listener, err := net.Listen("tcp", ":"+strconv.Itoa(port))
		if err == nil {
			return listener, nil
		}
	}
	return nil, fmt.Errorf("no available nonprivileged listener port outside kernel ephemeral range %d-%d", low, high)
}
