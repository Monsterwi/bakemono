package proxy

import (
	"net/http"
	"sort"
	"strings"
)

// GetCacheKey treats query item order as irrelevant under the origin contract.
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
	key := "http-v3:" + scheme + "://" + authority + path
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		items := strings.Split(r.URL.RawQuery, "&")
		sort.Strings(items)
		key += "?" + strings.Join(items, "&")
	}
	return key
}
