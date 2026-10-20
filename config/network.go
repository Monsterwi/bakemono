package config

import (
	"fmt"
	"net/url"
	"strconv"
)

func (c *Config) networkFields() []struct {
	name               string
	value              *int
	fallback, min, max int
} {
	return []struct {
		name               string
		value              *int
		fallback, min, max int
	}{
		{"interval", &c.HealthCheckInterval, 0, 0, 86400},
		{"timeout", &c.HealthCheckTimeout, 3, 1, 60},
		{"failures", &c.HealthCheckFailures, 2, 1, 1000},
		{"successes", &c.HealthCheckSuccesses, 2, 1, 1000},
	}
}
func (c *Config) networkDefaults() {
	for _, f := range c.networkFields() {
		if *f.value == 0 {
			*f.value = f.fallback
		}
	}
	if c.HealthCheckPath == "" {
		c.HealthCheckPath = "/"
	}
}
func (c *Config) validateNetwork() error {
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return fmt.Errorf("TLS certificate and key must both be configured")
	}
	for _, f := range c.networkFields() {
		if *f.value < f.min || *f.value > f.max {
			return fmt.Errorf("proxy.config.http.health.%s must be between %d and %d", f.name, f.min, f.max)
		}
	}
	path, err := url.ParseRequestURI(c.HealthCheckPath)
	if err != nil || path.IsAbs() || path.Host != "" || c.HealthCheckPath[0] != '/' || path.Fragment != "" {
		return fmt.Errorf("health path must be an absolute request path")
	}
	return nil
}
func (c *Config) loadNetwork(rc *RecordsConfig) error {
	c.TLSCertFile = rc.GetString("proxy.config.ssl.cert_file", "")
	c.TLSKeyFile = rc.GetString("proxy.config.ssl.key_file", "")
	c.UpstreamCAFile = rc.GetString("proxy.config.http.upstream_ca_file", "")
	c.HealthCheckPath = rc.GetString("proxy.config.http.health.path", c.HealthCheckPath)
	for _, f := range c.networkFields() {
		key := "proxy.config.http.health." + f.name
		if rc.Has(key) {
			value, err := strconv.Atoi(rc.GetString(key, ""))
			if err != nil {
				return fmt.Errorf("invalid integer %s: %w", key, err)
			}
			*f.value = value
		}
	}
	return c.validateNetwork()
}
