package logger

import (
	"net/http"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ContextKey is the type for context keys used in logging
type ContextKey string

const (
	// CacheStatusKey is the context key for cache status
	CacheStatusKey ContextKey = "cache_status"
)

// responseWriter wraps http.ResponseWriter to capture status code and bytes written
type responseWriter struct {
	http.ResponseWriter
	statusCode   int
	written      int64
	wroteOnce    bool
	customFields map[string]interface{} // 支持任意自定义字段
}

// newResponseWriter creates a new responseWriter
func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{
		ResponseWriter: w,
		statusCode:     http.StatusOK, // default status
		customFields:   make(map[string]interface{}),
	}
}

// Unwrap lets ResponseController reach streaming and connection capabilities.
func (rw *responseWriter) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

// FlushError records the implicit 200 committed by a streaming flush.
func (rw *responseWriter) FlushError() error {
	if !rw.wroteOnce {
		rw.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(rw.ResponseWriter).Flush()
}

// WriteHeader captures the status code
func (rw *responseWriter) WriteHeader(code int) {
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		if !rw.wroteOnce {
			rw.ResponseWriter.WriteHeader(code)
		}
		return
	}
	if !rw.wroteOnce {
		rw.statusCode = code
		rw.wroteOnce = true
		rw.ResponseWriter.WriteHeader(code)
	}
}

// Write captures the bytes written and ensures WriteHeader is called
func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.wroteOnce {
		rw.wroteOnce = true
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.written += int64(n)
	return n, err
}

// SetField sets a custom field for logging
func (rw *responseWriter) SetField(key string, value interface{}) {
	rw.customFields[key] = value
}

// GetField gets a custom field value
func (rw *responseWriter) GetField(key string) (interface{}, bool) {
	val, ok := rw.customFields[key]
	return val, ok
}

// GetAllFields returns all custom fields
func (rw *responseWriter) GetAllFields() map[string]interface{} {
	return rw.customFields
}

// SetCacheStatus sets the cache status for this response (便捷方法)
func (rw *responseWriter) SetCacheStatus(status string) {
	rw.SetField("cache_status", status)
}

// GetCacheStatus gets the cache status for this response (便捷方法)
func (rw *responseWriter) GetCacheStatus() string {
	if val, ok := rw.GetField("cache_status"); ok {
		if str, ok := val.(string); ok {
			return str
		}
	}
	return "-"
}

// compactTimeEncoder encodes time in compact format: 20260106163814
func compactTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.Format("20060102150405"))
}

// AccessLogMiddleware creates a middleware for logging HTTP access
// Compatible with standard net/http and supports cache_status from context
func AccessLogMiddleware(accessLogPath string) func(http.Handler) http.Handler {
	// Create dedicated access logger
	config := zap.NewProductionConfig()

	if len(accessLogPath) != 0 {
		os.MkdirAll(filepath.Dir(accessLogPath), 0755)
		config.OutputPaths = []string{accessLogPath}
		config.ErrorOutputPaths = []string{accessLogPath}
	}

	// Configure encoder for access log - use console format
	// config.Encoding = "console" // 使用 console 格式而不是 JSON

	// Configure encoder for access log
	config.EncoderConfig.EncodeTime = compactTimeEncoder // 紧凑时间格式：20260106163814
	config.EncoderConfig.TimeKey = "ts"
	config.EncoderConfig.MessageKey = ""         // 禁用 msg 字段
	config.EncoderConfig.LevelKey = ""           // 禁用 level 字段
	config.EncoderConfig.ConsoleSeparator = "\t" // 使用 Tab 分隔字段

	// Don't need caller and stacktrace for access logs
	config.DisableCaller = true
	config.DisableStacktrace = true

	accessLogger, err := config.Build()
	if err != nil {
		// Fallback to global logger if access logger creation fails
		if Logger != nil {
			Logger.Errorf("Failed to create access logger: %v", err)
		}
		accessLogger = zap.L()
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Wrap ResponseWriter to capture status code and custom fields
			ww := newResponseWriter(w)

			// Call next handler
			next.ServeHTTP(ww, r)

			// Calculate latency in milliseconds
			latency := time.Since(start)
			latencyMs := float64(latency.Microseconds()) / 1000.0

			// PURGE selectors may carry credentials; audit outcome without the URL.
			path, query := r.URL.Path, r.URL.RawQuery
			if r.Method == "PURGE" {
				path = "[redacted]"
				query = ""
			}

			// Build base log fields
			fields := []zapcore.Field{
				zap.Int("status", ww.statusCode),
				zap.String("method", r.Method),
				zap.String("path", path),
				zap.String("query", query),
				zap.String("remote_addr", r.RemoteAddr),
				zap.String("user-agent", r.UserAgent()),
				zap.Float64("latency", latencyMs), // 单位：毫秒
				zap.Int64("bytes", ww.written),
			}

			// Add all custom fields dynamically
			for key, value := range ww.GetAllFields() {
				fields = append(fields, zap.Any(key, value))
			}

			// Log access information (空消息，因为已禁用 msg 字段)
			accessLogger.Info("", fields...)
		})
	}
}

// SetCacheStatus sets the cache status on the response writer
// It accepts http.ResponseWriter and sets the cache status if it's our wrapped writer
func SetCacheStatus(w http.ResponseWriter, status string) {
	SetLogField(w, "cache_status", status)
}

// SetLogField sets a custom field on the response writer for logging
// This is the generic API for adding any custom field to access logs
func SetLogField(w http.ResponseWriter, key string, value interface{}) {
	for {
		if rw, ok := w.(*responseWriter); ok {
			rw.SetField(key, value)
			return
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = u.Unwrap()
	}
}

// GetLogField gets a custom field from the response writer
func GetLogField(w http.ResponseWriter, key string) (interface{}, bool) {
	if rw, ok := w.(*responseWriter); ok {
		return rw.GetField(key)
	}
	return nil, false
}
