package proxy

import (
	"net/http"
	"net/url"
)

func GetCacheKey(r *http.Request) string {
	includeParams := []string{"blk", "type"}
	params := r.URL.Query()
	values := make(url.Values)
	for _, p := range includeParams {
		if v := params.Get(p); v != "" {
			values.Add(p, v)
		}
	}
	cacheKey := r.URL.Path
	if t := values.Encode(); t != "" {
		cacheKey += "?" + t
	}
	return cacheKey
}
