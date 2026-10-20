package server

import (
	"crypto/tls"
	"fmt"
)

func (s *Server) configureNetwork() error {
	if s.config.TLSCertFile != "" {
		certificate, err := tls.LoadX509KeyPair(s.config.TLSCertFile, s.config.TLSKeyFile)
		if err != nil {
			return fmt.Errorf("load listener TLS certificate: %w", err)
		}
		s.tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	}
	return nil
}
