package server

import (
	"crypto/tls"
	"fmt"
	"time"

	"github.com/Monsterwi/razor/proxy"
)

func (s *Server) configureNetwork() error {
	if s.config.TLSCertFile != "" {
		certificate, err := tls.LoadX509KeyPair(s.config.TLSCertFile, s.config.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("load listener TLS certificate: %w", err)
		}
		s.tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	}
	return s.proxy.StartHealthChecks(proxy.HealthConfig{
		Interval: time.Duration(s.config.HealthCheckInterval) * time.Second,
		Timeout:  time.Duration(s.config.HealthCheckTimeout) * time.Second,
		Failures: s.config.HealthCheckFailures, Successes: s.config.HealthCheckSuccesses, Path: s.config.HealthCheckPath,
	})
}
