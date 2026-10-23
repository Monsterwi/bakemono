package server

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Monsterwi/razor/cache"
)

func requestCacheable(r *http.Request) bool {
	if (r.Method != http.MethodGet && r.Method != http.MethodHead) || r.ContentLength != 0 || len(r.TransferEncoding) != 0 || (r.Body != nil && r.Body != http.NoBody) {
		return false
	}
	for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Range", "If-Range", "If-Match", "If-Unmodified-Since"} {
		if hasHeader(r.Header, name) {
			return false
		}
	}
	if connectionNominates(r.Header, "Cache-Control", "Pragma", "If-None-Match", "If-Modified-Since", "Accept-Encoding", "Accept-Language") {
		return false
	}
	if hasHeader(r.Header, "Pragma") && !strings.EqualFold(r.Header.Get("Pragma"), "no-cache") {
		return false
	}
	_, ok := cacheDirectives(r.Header, true)
	return ok
}

func connectionNominates(h http.Header, names ...string) bool {
	for _, line := range h.Values("Connection") {
		for _, field := range strings.Split(line, ",") {
			for _, name := range names {
				if strings.EqualFold(strings.TrimSpace(field), name) {
					return true
				}
			}
		}
	}
	return false
}

func cacheDirectives(h http.Header, request bool) (map[string]string, bool) {
	directives := make(map[string]string)
	for _, line := range h.Values("Cache-Control") {
		for _, raw := range strings.Split(line, ",") {
			name, value, valued := strings.Cut(strings.TrimSpace(raw), "=")
			name, value = strings.ToLower(strings.TrimSpace(name)), strings.TrimSpace(value)
			if _, duplicate := directives[name]; duplicate {
				return nil, false
			}
			numeric := name == "max-age" || (!request && name == "s-maxage") || (request && name == "min-fresh")
			flag := name == "no-cache" || (!request && (name == "public" || name == "must-revalidate" || name == "proxy-revalidate" || name == "immutable" || name == "no-transform"))
			if numeric {
				if !valued {
					return nil, false
				}
				if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
					value = value[1 : len(value)-1]
				}
				if _, ok := deltaSeconds(value); !ok {
					return nil, false
				}
			} else if !flag || valued {
				return nil, false
			}
			directives[name] = value
		}
	}
	return directives, true
}

func hasHeader(h http.Header, name string) bool {
	for key := range h {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

type freshness struct {
	age, lifetime time.Duration
	validate      bool
	evaluated     time.Time
}

// responsePolicy is shared by admission, lookup and successful validation.
// Unsupported directives deliberately bypass; stale responses are never served.
func responsePolicy(info *cache.CacheHTTPInfo, now time.Time) (freshness, bool) {
	p := freshness{evaluated: now}
	if info == nil || info.Status != http.StatusOK || info.RequestMethod != http.MethodGet {
		return p, false
	}
	h := info.ResponseHeaders
	if _, ok := varyFields(h); !ok {
		return p, false
	}
	if connectionNominates(h, "Cache-Control", "Age", "Date", "Set-Cookie", "Vary", "Content-Range", "Trailer", "ETag", "Last-Modified") {
		return p, false
	}
	for _, name := range []string{"Set-Cookie", "Content-Range", "Trailer"} {
		if hasHeader(h, name) {
			return p, false
		}
	}
	directives, ok := cacheDirectives(h, false)
	if !ok {
		return p, false
	}
	_, public := directives["public"]
	lifetime, shared := directives["s-maxage"]
	if !public && !shared {
		return p, false
	}
	if !shared {
		lifetime = directives["max-age"]
	}
	p.lifetime, ok = deltaSeconds(lifetime)
	if !ok {
		return p, false
	}
	_, p.validate = directives["no-cache"]
	if values := h.Values("Age"); len(values) > 0 {
		if len(values) != 1 {
			return p, false
		}
		p.age, ok = deltaSeconds(values[0])
		if !ok {
			return p, false
		}
	}
	delay := info.ResponseTime.Sub(info.RequestTime)
	resident := now.Sub(info.ResponseTime)
	if delay < 0 || resident < 0 {
		return p, false
	}
	p.age = addAge(p.age, delay)
	if values := h.Values("Date"); len(values) > 0 {
		if len(values) != 1 {
			return p, false
		}
		date, err := http.ParseTime(values[0])
		if err != nil {
			return p, false
		}
		p.age = max(p.age, info.ResponseTime.Sub(date))
	}
	p.age = addAge(p.age, resident)
	return p, true
}

func addAge(a, b time.Duration) time.Duration {
	if a > time.Duration(math.MaxInt64)-b {
		return time.Duration(math.MaxInt64)
	}
	return a + b
}

func requestFresh(r *http.Request, info *cache.CacheHTTPInfo, now time.Time) bool {
	p, ok := responsePolicy(info, now)
	return ok && requestPolicyFresh(r, p, now)
}

// The lookup policy is request-local: age and request constraints are never cached.
func requestPolicyFresh(r *http.Request, p freshness, now time.Time) bool {
	age, valid := p.currentAge(now)
	if !valid {
		return false
	}
	p.age = age
	if p.validate || p.age >= p.lifetime || hasHeader(r.Header, "Pragma") {
		return false
	}
	directives, ok := cacheDirectives(r.Header, true)
	if !ok {
		return false
	}
	if _, force := directives["no-cache"]; force {
		return false
	}
	if v, exists := directives["max-age"]; exists {
		age, _ := deltaSeconds(v)
		if p.age > age {
			return false
		}
	}
	if v, exists := directives["min-fresh"]; exists {
		age, _ := deltaSeconds(v)
		if age >= p.lifetime-p.age {
			return false
		}
	}
	return true
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

// Reuse parsed directives, but advance age through scheduling and buffer waits.
func (p freshness) currentAge(now time.Time) (time.Duration, bool) {
	elapsed := now.Sub(p.evaluated)
	if elapsed < 0 {
		return 0, false
	}
	return addAge(p.age, elapsed), true
}
