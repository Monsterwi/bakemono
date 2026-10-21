package config

import (
	"fmt"
	"strconv"
)

func (c *Config) validateRAM() error {
	if c.RAMCacheAlgorithm != "otter" && c.RAMCacheAlgorithm != "s3-fifo" {
		return fmt.Errorf("proxy.config.cache.ram_cache_algorithm must be otter or s3-fifo")
	}
	if c.RAMCacheBytes < 0 || c.RAMCacheBytes > 1<<40 {
		return fmt.Errorf("proxy.config.cache.ram_cache_bytes must be between 0 and 1099511627776")
	}
	return nil
}
func (c *Config) loadRAM(rc *RecordsConfig) error {
	const algorithm = "proxy.config.cache.ram_cache_algorithm"
	const capacity = "proxy.config.cache.ram_cache_bytes"
	if v, ok := rc.configs[algorithm]; ok {
		if v.Type != "STRING" {
			return fmt.Errorf("%s requires STRING", algorithm)
		}
		c.RAMCacheAlgorithm = v.Value
	}
	if v, ok := rc.configs[capacity]; ok {
		if v.Type != "INT" {
			return fmt.Errorf("%s requires INT", capacity)
		}
		n, err := strconv.Atoi(v.Value)
		if err != nil {
			return fmt.Errorf("invalid %s: %w", capacity, err)
		}
		c.RAMCacheBytes = n
	}
	return c.validateRAM()
}
