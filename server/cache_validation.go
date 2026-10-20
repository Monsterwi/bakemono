package server

import (
	"net/http"
	"strings"

	"github.com/Monsterwi/razor/cache"
)

func validETag(tag string) bool {
	tag = strings.TrimPrefix(tag, "W/")
	if len(tag) < 2 || tag[0] != '"' || tag[len(tag)-1] != '"' {
		return false
	}
	for _, c := range tag[1 : len(tag)-1] {
		if c == '"' || c < 0x21 || c == 0x7f {
			return false
		}
	}
	return true
}

func mergeResponseHeaders(old, updated http.Header) http.Header {
	merged := old.Clone()
	for name, values := range updated {
		switch http.CanonicalHeaderKey(name) {
		case "Content-Length", "Content-Range", "Transfer-Encoding", "Trailer":
			continue
		}
		merged[name] = append([]string(nil), values...)
	}
	// A validation response starts a new age calculation; an old Age is not carried.
	if !hasHeader(updated, "Age") {
		merged.Del("Age")
	}
	return merged
}

func mergeValidation(previous, validation *cache.CacheHTTPInfo, request *http.Request) (*cache.CacheHTTPInfo, bool) {
	for _, name := range []string{"Content-Range", "Trailer"} {
		if hasHeader(validation.ResponseHeaders, name) {
			return nil, false
		}
	}
	if hasHeader(validation.ResponseHeaders, "Content-Encoding") && strings.Join(validation.ResponseHeaders.Values("Content-Encoding"), ",") != strings.Join(previous.ResponseHeaders.Values("Content-Encoding"), ",") {
		return nil, false
	}

	storedTag := previous.ResponseHeaders.Get("ETag")
	if received := validation.ResponseHeaders.Get("ETag"); received != "" {
		if len(validation.ResponseHeaders.Values("ETag")) != 1 || !validETag(received) || !validETag(storedTag) {
			return nil, false
		}
		if strings.HasPrefix(received, "W/") {
			if strings.TrimPrefix(received, "W/") != strings.TrimPrefix(storedTag, "W/") {
				return nil, false
			}
		} else if received != storedTag {
			return nil, false
		}
	}
	if tag := request.Header.Get("If-None-Match"); tag != "" {
		if !validETag(tag) || tag != storedTag {
			return nil, false
		}
	} else {
		sent, err := http.ParseTime(request.Header.Get("If-Modified-Since"))
		if err != nil {
			return nil, false
		}
		stored, err := http.ParseTime(previous.ResponseHeaders.Get("Last-Modified"))
		if err != nil || !sent.Equal(stored) {
			return nil, false
		}
		if received := validation.ResponseHeaders.Get("Last-Modified"); received != "" {
			modified, err := http.ParseTime(received)
			if err != nil || !modified.Equal(stored) {
				return nil, false
			}
		}
	}
	merged := *previous
	merged.RequestTime, merged.ResponseTime = validation.RequestTime, validation.ResponseTime
	merged.ResponseHeaders = mergeResponseHeaders(previous.ResponseHeaders, validation.ResponseHeaders)
	_, valid := responsePolicy(&merged, merged.ResponseTime)
	return &merged, valid
}

func clientNotModified(r *http.Request, h http.Header) bool {
	if hasHeader(r.Header, "If-None-Match") {
		value := strings.TrimSpace(strings.Join(r.Header.Values("If-None-Match"), ","))
		if value == "*" {
			return true
		}
		stored := h.Get("ETag")
		if !validETag(stored) {
			return false
		}
		matched := false
		for value != "" {
			weak := strings.HasPrefix(value, "W/")
			start := 0
			if weak {
				start = 2
			}
			if len(value) <= start || value[start] != '"' {
				return false
			}
			end := strings.IndexByte(value[start+1:], '"')
			if end < 0 {
				return false
			}
			end += start + 2
			tag := value[:end]
			if !validETag(tag) {
				return false
			}
			if strings.TrimPrefix(tag, "W/") == strings.TrimPrefix(stored, "W/") {
				matched = true
			}
			value = strings.TrimSpace(value[end:])
			if value == "" {
				return matched
			}
			if value[0] != ',' {
				return false
			}
			value = strings.TrimSpace(value[1:])
			if value == "" {
				return false
			}
		}
		return matched
	}
	modified, err := http.ParseTime(h.Get("Last-Modified"))
	if err != nil {
		return false
	}
	since, err := http.ParseTime(r.Header.Get("If-Modified-Since"))
	return err == nil && !modified.After(since)
}
