package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"

	"github.com/Monsterwi/razor/logger"
)

func recordPublicRequestIdentity(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{"Traceparent", "X-Request-Id", "X-Correlation-Id"} {
		value := r.Header.Get(name)
		if value == "" {
			continue
		}
		if name == "Traceparent" {
			// Accept only the fixed W3C version-00 form; arbitrary baggage stays private.
			if len(value) != 55 || !strings.HasPrefix(value, "00-") || value[35] != '-' || value[52] != '-' || strings.ToLower(value) != value {
				continue
			}
			trace, parent, flags := value[3:35], value[36:52], value[53:55]
			if trace == strings.Repeat("0", 32) || parent == strings.Repeat("0", 16) {
				continue
			}
			if _, err := hex.DecodeString(trace + parent + flags); err != nil {
				continue
			}
		}
		// Correlate locally without retaining arbitrary client-supplied identifiers.
		logger.SetLogField(w, "client_"+strings.ReplaceAll(strings.ToLower(name), "-", "_")+"_sha256", fmt.Sprintf("%x", sha256.Sum256([]byte(value))))
	}
}
