package bakemono

import (
	"os"
	"path/filepath"
	"time"

	ginzap "github.com/gin-contrib/zap"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var logger *zap.SugaredLogger

func InitLogger(zapCfg zap.Config) (*zap.SugaredLogger, error) {
	for _, path := range append(zapCfg.OutputPaths, zapCfg.ErrorOutputPaths...) {
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return nil, err
		}
	}
	zapLogger, err := zapCfg.Build()
	if err != nil {
		return nil, err
	}
	zap.ReplaceGlobals(zapLogger)
	defer zapLogger.Sync()
	zap.L().Info("log construction succeeded")
	zap.S().Info("log construction succeeded [sugared]")

	logger = zapLogger.Sugar()
	return logger, nil
}

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
	logger, _ := config.Build()
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

func initTestingLogger() {
	if logger != nil {
		return
	}
	zapCfg := zap.NewDevelopmentConfig()
	InitLogger(zapCfg)
}
