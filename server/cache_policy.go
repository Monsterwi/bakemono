package server

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Monsterwi/razor/cache"
)

// shortcut: bypass request directives and validators until conditional revalidation is supported.
func requestCacheable(r *http.Request) bool {
	if r.Method != http.MethodGet || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		return false
	}
	for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Range", "If-Range", "If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "Cache-Control", "Pragma"} {
		if hasHeader(r.Header, name) {
			return false
		}
	}
	return true
}

func hasHeader(h http.Header, name string) bool {
	for key := range h {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

// responseAge accepts only the deliberately small shared-cache subset. Unknown or
// ambiguous directives bypass until their semantics are implemented.
func responseAge(info *cache.CacheHTTPInfo, now time.Time) (time.Duration, bool) {
	if info == nil || info.Status != http.StatusOK || info.RequestMethod != http.MethodGet {
		return 0, false
	}
	h := info.ResponseHeaders
	for _, name := range []string{"Set-Cookie", "Vary", "Content-Range", "Trailer"} {
		if hasHeader(h, name) {
			return 0, false
		}
	}
	directives := make(map[string]string)
	for _, line := range h.Values("Cache-Control") {
		for _, raw := range strings.Split(line, ",") {
			name, value, valued := strings.Cut(strings.TrimSpace(raw), "=")
			name = strings.ToLower(strings.TrimSpace(name))
			value = strings.TrimSpace(value)
			if _, duplicate := directives[name]; duplicate {
				return 0, false
			}
			switch name {
			case "public", "must-revalidate", "proxy-revalidate", "immutable", "no-transform":
				if valued {
					return 0, false
				}
			case "max-age", "s-maxage":
				if !valued {
					return 0, false
				}
				if strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") && len(value) >= 2 {
					value = value[1 : len(value)-1]
				}
				if _, ok := deltaSeconds(value); !ok {
					return 0, false
				}
			default:
				return 0, false
			}
			directives[name] = value
		}
	}
	_, public := directives["public"]
	lifetimeText, shared := directives["s-maxage"]
	if !public && !shared {
		return 0, false
	}
	if !shared {
		lifetimeText = directives["max-age"]
	}
	lifetime, ok := deltaSeconds(lifetimeText)
	if !ok || lifetime <= 0 {
		return 0, false
	}
	age := time.Duration(0)
	if values := h.Values("Age"); len(values) > 0 {
		if len(values) != 1 {
			return 0, false
		}
		age, ok = deltaSeconds(values[0])
		if !ok {
			return 0, false
		}
	}
	delay := info.ResponseTime.Sub(info.RequestTime)
	if delay < 0 {
		return 0, false
	}
	// Bound before addition so an enormous Age cannot overflow into a fresh value.
	if age >= lifetime || delay >= lifetime-age {
		return 0, false
	}
	age += delay
	if values := h.Values("Date"); len(values) > 0 {
		if len(values) != 1 {
			return 0, false
		}
		date, err := http.ParseTime(values[0])
		if err != nil {
			return 0, false
		}
		apparent := info.ResponseTime.Sub(date)
		if apparent > age {
			age = apparent
		}
	}
	resident := now.Sub(info.ResponseTime)
	if resident < 0 {
		return 0, false
	}
	if age >= lifetime || resident >= lifetime-age {
		return 0, false
	}
	return age + resident, true
}

func deltaSeconds(value string) (time.Duration, bool) {
	if value == "" {
		return 0, false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	seconds, err := strconv.ParseUint(value, 10, 63)
	if err != nil || seconds > uint64(math.MaxInt64/int64(time.Second)) {
		return 0, false
	}
	return time.Duration(seconds) * time.Second, true
}
