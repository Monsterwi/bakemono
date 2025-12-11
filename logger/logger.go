package logger

import (
	"os"
	"path/filepath"
	"time"

	ginzap "github.com/gin-contrib/zap"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var debug = true

// Logger is the global logger instance
var Logger *zap.SugaredLogger

func init() {
	if debug {
		InitTestingLogger()
	}
}

// InitLogger initializes the logger with the given zap config
func InitLogger(zapCfg zap.Config) (*zap.SugaredLogger, error) {
	for _, path := range append(zapCfg.OutputPaths, zapCfg.ErrorOutputPaths...) {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return nil, err
		}
	}

	// Enable caller information
	zapCfg.EncoderConfig.CallerKey = "caller"
	zapCfg.EncoderConfig.StacktraceKey = "stacktrace"

	// Build logger with caller and stacktrace enabled
	// AddCallerSkip(1) skips the wrapper functions (Info, Infof, etc.) to show the actual caller
	zapLogger, err := zapCfg.Build(
		zap.AddCaller(),
		zap.AddCallerSkip(1),                  // Skip wrapper functions to show actual caller location
		zap.AddStacktrace(zapcore.ErrorLevel), // Add stacktrace for errors
	)
	if err != nil {
		return nil, err
	}
	zap.ReplaceGlobals(zapLogger)
	defer zapLogger.Sync()
	zap.L().Info("log construction succeeded")
	zap.S().Info("log construction succeeded [sugared]")

	Logger = zapLogger.Sugar()
	return Logger, nil
}

// GetGinLog returns a Gin middleware for logging
// TODO: compatible with nginx log format
func GetGinLog(accessLogPath string) gin.HandlerFunc {
	config := zap.NewProductionConfig()
	if len(accessLogPath) != 0 {
		os.MkdirAll(filepath.Dir(accessLogPath), 0755)
		config.OutputPaths = []string{accessLogPath}
		config.ErrorOutputPaths = []string{accessLogPath}
	}
	// config.Encoding = "console"
	config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	config.EncoderConfig.CallerKey = "caller"
	config.EncoderConfig.StacktraceKey = "stacktrace"
	logger, _ := config.Build(
		zap.AddCaller(),
		zap.AddStacktrace(zapcore.ErrorLevel),
	)
	return ginzap.GinzapWithConfig(logger, &ginzap.Config{
		UTC:        true,
		TimeFormat: time.RFC3339,
		Context: ginzap.Fn(func(c *gin.Context) []zapcore.Field {
			var hit string
			if h, ok := c.Get("CACHE_STATUS"); ok {
				hit = h.(string)
			}
			return []zapcore.Field{zap.String("CACHE_STATUS", hit)}
		}),
	})
}

// InitTestingLogger initializes a development logger for testing
func InitTestingLogger() {
	if Logger != nil {
		return
	}
	zapCfg := zap.NewDevelopmentConfig()
	// Development config already has caller enabled, but we ensure it's set
	zapCfg.EncoderConfig.CallerKey = "caller"
	zapCfg.EncoderConfig.StacktraceKey = "stacktrace"
	InitLogger(zapCfg)
}

// Info logs an info message
// Caller skip is 1 because we're wrapping the zap logger
func Info(args ...interface{}) {
	if Logger != nil {
		Logger.Info(args...)
	}
}

// Infof logs a formatted info message
func Infof(template string, args ...interface{}) {
	if Logger != nil {
		Logger.Infof(template, args...)
	}
}

// Error logs an error message
func Error(args ...interface{}) {
	if Logger != nil {
		Logger.Error(args...)
	}
}

// Errorf logs a formatted error message
func Errorf(template string, args ...interface{}) {
	if Logger != nil {
		Logger.Errorf(template, args...)
	}
}

// Warn logs a warning message
func Warn(args ...interface{}) {
	if Logger != nil {
		Logger.Warn(args...)
	}
}

// Warnf logs a formatted warning message
func Warnf(template string, args ...interface{}) {
	if Logger != nil {
		Logger.Warnf(template, args...)
	}
}

// Debug logs a debug message
func Debug(args ...interface{}) {
	if Logger != nil {
		Logger.Debug(args...)
	}
}

// Debugf logs a formatted debug message
func Debugf(template string, args ...interface{}) {
	if Logger != nil {
		Logger.Debugf(template, args...)
	}
}

// Fatal logs a fatal message and exits
func Fatal(args ...interface{}) {
	if Logger != nil {
		Logger.Fatal(args...)
	}
}

// Fatalf logs a formatted fatal message and exits
func Fatalf(template string, args ...interface{}) {
	if Logger != nil {
		Logger.Fatalf(template, args...)
	}
}
