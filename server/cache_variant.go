package server

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

// shortcut: only unchanged negotiation fields are supported; add other Vary
// fields only with an explicit mapping to the effective outbound request.
func varyFields(h http.Header) ([]string, bool) {
	fields := []string{}
	for _, line := range h.Values("Vary") {
		for _, field := range strings.Split(line, ",") {
			field = http.CanonicalHeaderKey(strings.TrimSpace(field))
			if field != "Accept-Encoding" && field != "Accept-Language" {
				return nil, false
			}
			if !containsField(fields, field) {
				fields = append(fields, field)
			}
		}
	}
	sort.Strings(fields)
	return fields, true
}

func containsField(fields []string, field string) bool {
	for _, f := range fields {
		if f == field {
			return true
		}
	}
	return false
}

func representationKey(selector []byte, h http.Header, fields []string) []byte {
	if len(fields) == 0 {
		return selector
	}
	values := make([][]string, len(fields))
	for i, field := range fields {
		values[i] = h.Values(field)
	}
	schema, _ := json.Marshal(fields)
	variant, _ := json.Marshal(values)
	return []byte(fmt.Sprintf("%s\nvariant:%x:%x", selector, sha256.Sum256(schema), sha256.Sum256(variant)))
}

const maxHTTPVariants = 16

func sameVary(a, b http.Header) bool {
	first, ok := varyFields(a)
	if !ok {
		return false
	}
	second, ok := varyFields(b)
	return ok && strings.Join(first, ",") == strings.Join(second, ",")
}

func sameRepresentationSchema(selector, a, b []byte) bool {
	prefix := func(key []byte) string {
		if string(key) == string(selector) {
			return string(selector)
		}
		value := string(key)
		i := strings.LastIndexByte(value, ':')
		if i < 0 {
			return value
		}
		return value[:i]
	}
	return prefix(a) == prefix(b)
}
