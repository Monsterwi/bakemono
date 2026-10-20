package proxy

import "net/http"

// GetCacheKey preserves the complete request target; URL equivalence is not assumed.
func GetCacheKey(r *http.Request) string {
	scheme := r.URL.Scheme
	if scheme == "" {
		scheme = "http"
		if r.TLS != nil {
			scheme = "https"
		}
	}
	authority := r.Host
	if authority == "" {
		authority = r.URL.Host
	}
	path := r.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	key := "http-v2:" + scheme + "://" + authority + path
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		key += "?" + r.URL.RawQuery
	}
	return key
}
