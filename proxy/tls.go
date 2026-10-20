package proxy

import (
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"os"
)

// WithCAFile adds private origin roots to system trust. Certificate chain and
// hostname verification remain enabled for both traffic and health requests.
func WithCAFile(path string) Option {
	return func(h *HTTPProxy) error {
		if path == "" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read upstream CA: %w", err)
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			return fmt.Errorf("load system roots: %w", err)
		}
		if !roots.AppendCertsFromPEM(data) {
			return fmt.Errorf("upstream CA file contains no certificates")
		}
		h.roots = roots
		h.caDigest = sha256.Sum256(data)
		return nil
	}
}
