package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/Monsterwi/razor/logger"
)

// RecordsConfig represents the parsed records.config
type RecordsConfig struct {
	configs map[string]RecordValue
}

// RecordValue stores a configuration value with its type
type RecordValue struct {
	Type  string // STRING, INT, FLOAT
	Value string
}

// NewRecordsConfig creates a new RecordsConfig instance
func NewRecordsConfig() *RecordsConfig {
	return &RecordsConfig{
		configs: make(map[string]RecordValue),
	}
}

// LoadRecordsConfig loads and parses the records.config file
func LoadRecordsConfig(filePath string) (*RecordsConfig, error) {
	rc := NewRecordsConfig()

	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open records.config: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse CONFIG lines: CONFIG <name> <type> <value>
		if strings.HasPrefix(line, "CONFIG ") {
			parts := strings.Fields(line)
			if len(parts) < 4 {
				logger.Warnf("Invalid config line %d: %s", lineNum, line)
				continue
			}

			configName := parts[1]
			configType := parts[2]
			configValue := strings.Join(parts[3:], " ")

			rc.configs[configName] = RecordValue{
				Type:  configType,
				Value: configValue,
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading records.config: %w", err)
	}

	logger.Infof("Loaded %d configuration entries from %s", len(rc.configs), filePath)
	return rc, nil
}

// GetString retrieves a string value from configuration
func (rc *RecordsConfig) GetString(key string, defaultValue string) string {
	if val, ok := rc.configs[key]; ok {
		return val.Value
	}
	return defaultValue
}

// GetInt retrieves an integer value from configuration
func (rc *RecordsConfig) GetInt(key string, defaultValue int) int {
	if val, ok := rc.configs[key]; ok {
		if i, err := strconv.Atoi(val.Value); err == nil {
			return i
		}
		logger.Warnf("Failed to parse int for key %s: %s", key, val.Value)
	}
	return defaultValue
}

// GetFloat retrieves a float value from configuration
func (rc *RecordsConfig) GetFloat(key string, defaultValue float64) float64 {
	if val, ok := rc.configs[key]; ok {
		if f, err := strconv.ParseFloat(val.Value, 64); err == nil {
			return f
		}
		logger.Warnf("Failed to parse float for key %s: %s", key, val.Value)
	}
	return defaultValue
}

// GetStringSlice retrieves a comma-separated string as a slice
func (rc *RecordsConfig) GetStringSlice(key string, defaultValue []string) []string {
	if val, ok := rc.configs[key]; ok {
		parts := strings.Split(val.Value, ",")
		result := make([]string, 0, len(parts))
		for _, part := range parts {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				result = append(result, trimmed)
			}
		}
		if len(result) > 0 {
			return result
		}
	}
	return defaultValue
}

// Has checks if a configuration key exists
func (rc *RecordsConfig) Has(key string) bool {
	_, ok := rc.configs[key]
	return ok
}

// Print prints all configuration entries (for debugging)
func (rc *RecordsConfig) Print() {
	logger.Info("=== Records Configuration ===")
	for key, val := range rc.configs {
		logger.Infof("  %s = %s (%s)", key, val.Value, val.Type)
	}
	logger.Info("============================")
}
