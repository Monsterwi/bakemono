package logger

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestAccessLogMiddleware(t *testing.T) {
	// Create temporary log directory
	tmpDir := t.TempDir()
	accessLogPath := filepath.Join(tmpDir, "access.log")

	// Create a simple handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Set cache status on response writer
		SetCacheStatus(w, "HIT")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello, World!"))
	})

	// Wrap with access log middleware
	loggedHandler := AccessLogMiddleware(accessLogPath)(handler)

	// Create test request
	req := httptest.NewRequest("GET", "/test?foo=bar", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	req.Header.Set("User-Agent", "TestAgent/1.0")

	// Create response recorder
	w := httptest.NewRecorder()

	// Serve request
	loggedHandler.ServeHTTP(w, req)

	// Check response
	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected status 200, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "Hello, World!" {
		t.Errorf("Expected 'Hello, World!', got '%s'", string(body))
	}

	// Check if log file was created
	if _, err := os.Stat(accessLogPath); os.IsNotExist(err) {
		t.Error("Access log file was not created")
	}

	// Read log file content
	logContent, err := os.ReadFile(accessLogPath)
	if err != nil {
		t.Fatalf("Failed to read access log: %v", err)
	}

	// Verify log content contains expected fields
	logStr := string(logContent)
	expectedFields := []string{
		"GET",           // method
		"/test",         // path
		"foo=bar",       // query
		"192.168.1.1",   // IP
		"TestAgent/1.0", // user-agent
		"HIT",           // cache_status
		"\"ts\":",       // timestamp field
		"\"latency\":",  // latency field (应为数字)
	}

	// 不应包含的字段
	unexpectedFields := []string{
		"\"level\":", // 已移除
		"\"msg\":",   // 已移除
		"\"time\":",  // 已移除（只保留 ts）
	}

	for _, field := range expectedFields {
		if len(logStr) > 0 && !contains(logStr, field) {
			t.Errorf("Log does not contain expected field: %s\nLog content: %s", field, logStr)
		}
	}

	for _, field := range unexpectedFields {
		if len(logStr) > 0 && contains(logStr, field) {
			t.Errorf("Log should not contain field: %s\nLog content: %s", field, logStr)
		}
	}
}

func TestSetCacheStatus(t *testing.T) {
	w := httptest.NewRecorder()
	rw := newResponseWriter(w)

	// Set cache status
	SetCacheStatus(rw, "MISS")

	// Verify it's set
	status := rw.GetCacheStatus()
	if status != "MISS" {
		t.Errorf("Expected cache status 'MISS', got '%s'", status)
	}

	// Test with non-wrapped writer (should not panic)
	plainWriter := httptest.NewRecorder()
	SetCacheStatus(plainWriter, "TEST") // Should not crash
}

func TestSetLogField(t *testing.T) {
	w := httptest.NewRecorder()
	rw := newResponseWriter(w)

	// Set various custom fields
	SetLogField(rw, "upstream_addr", "192.168.1.100:8080")
	SetLogField(rw, "request_id", "req-12345")
	SetLogField(rw, "upstream_time", 0.123)
	SetLogField(rw, "cache_key", "/path/to/resource")

	// Verify fields are set
	if val, ok := GetLogField(rw, "upstream_addr"); !ok || val != "192.168.1.100:8080" {
		t.Errorf("Expected upstream_addr to be '192.168.1.100:8080', got '%v'", val)
	}

	if val, ok := GetLogField(rw, "request_id"); !ok || val != "req-12345" {
		t.Errorf("Expected request_id to be 'req-12345', got '%v'", val)
	}

	if val, ok := GetLogField(rw, "upstream_time"); !ok || val != 0.123 {
		t.Errorf("Expected upstream_time to be 0.123, got '%v'", val)
	}

	// Test non-existent field
	if _, ok := GetLogField(rw, "non_existent"); ok {
		t.Error("Expected non_existent field to not exist")
	}

	// Test with non-wrapped writer (should not panic)
	plainWriter := httptest.NewRecorder()
	SetLogField(plainWriter, "test", "value") // Should not crash
	if _, ok := GetLogField(plainWriter, "test"); ok {
		t.Error("Should not be able to get field from non-wrapped writer")
	}
}

func TestGetAllFields(t *testing.T) {
	w := httptest.NewRecorder()
	rw := newResponseWriter(w)

	// Set multiple fields
	SetLogField(rw, "field1", "value1")
	SetLogField(rw, "field2", 123)
	SetLogField(rw, "field3", true)

	// Get all fields
	fields := rw.GetAllFields()

	if len(fields) != 3 {
		t.Errorf("Expected 3 fields, got %d", len(fields))
	}

	if fields["field1"] != "value1" {
		t.Errorf("Expected field1 to be 'value1', got '%v'", fields["field1"])
	}

	if fields["field2"] != 123 {
		t.Errorf("Expected field2 to be 123, got '%v'", fields["field2"])
	}

	if fields["field3"] != true {
		t.Errorf("Expected field3 to be true, got '%v'", fields["field3"])
	}
}

func TestResponseWriter(t *testing.T) {
	// Create a test response writer
	w := httptest.NewRecorder()
	rw := newResponseWriter(w)

	// Check default status
	if rw.statusCode != http.StatusOK {
		t.Errorf("Expected default status 200, got %d", rw.statusCode)
	}

	// Write header
	rw.WriteHeader(http.StatusNotFound)
	if rw.statusCode != http.StatusNotFound {
		t.Errorf("Expected status 404, got %d", rw.statusCode)
	}

	// Write body
	n, err := rw.Write([]byte("test body"))
	if err != nil {
		t.Errorf("Write failed: %v", err)
	}
	if n != 9 {
		t.Errorf("Expected 9 bytes written, got %d", n)
	}
	if rw.written != 9 {
		t.Errorf("Expected written counter to be 9, got %d", rw.written)
	}

	// Try writing header again (should be ignored)
	rw.WriteHeader(http.StatusInternalServerError)
	if rw.statusCode != http.StatusNotFound {
		t.Errorf("Status should remain 404, got %d", rw.statusCode)
	}
}

func TestResponseWriterDefaultStatus(t *testing.T) {
	// Test that writing without explicit WriteHeader uses 200
	w := httptest.NewRecorder()
	rw := newResponseWriter(w)

	// Write body without calling WriteHeader
	rw.Write([]byte("test"))

	// Status should still be 200
	if rw.statusCode != http.StatusOK {
		t.Errorf("Expected default status 200, got %d", rw.statusCode)
	}
}

// Helper function to check if string contains substring
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && (s[:len(substr)] == substr || contains(s[1:], substr))))
}

func TestAccessLogPreservesResponseControllerFlush(t *testing.T) {
	flushed := make(chan error, 1)
	handler := AccessLogMiddleware(filepath.Join(t.TempDir(), "access.log"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("first"))
		flushed <- http.NewResponseController(w).Flush()
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if err := <-flushed; err != nil {
		t.Fatalf("flush through middleware: %v", err)
	}
	if !recorder.Flushed {
		t.Fatal("underlying response was not flushed")
	}
}

func TestAccessLogPreservesFinalStatusAfterEarlyHints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	handler := AccessLogMiddleware(path)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusTeapot)
	}))
	server := httptest.NewServer(handler)
	defer server.Close()
	resp, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("final status = %d", resp.StatusCode)
	}
}

func TestAccessLogRecordsStatusCommittedByFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	handler := AccessLogMiddleware(path)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusTeapot)
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entry struct {
		Status int `json:"status"`
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || entry.Status != http.StatusOK {
		t.Fatalf("wire status = %d; logged status = %d", recorder.Code, entry.Status)
	}
}
