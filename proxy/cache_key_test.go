package proxy

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
)

func TestCacheKeySeparatesCompleteRequestIdentity(t *testing.T) {
	targets := []string{"http://a.test/item?id=1", "http://a.test/item?id=2", "http://b.test/item?id=1", "https://a.test/item?id=1", "http://a.test/a%2Fb", "http://a.test/a/b", "http://a.test/item?a=1&b=2", "http://a.test/item?b=2&a=1", "http://a.test/item?", "http://a.test/item"}
	seen := map[string]string{}
	for _, target := range targets {
		req := httptest.NewRequest("GET", target, nil)
		key := GetCacheKey(req)
		if other, ok := seen[key]; ok {
			t.Fatalf("%s and %s share cache identity", other, target)
		}
		seen[key] = target
	}
	plain := httptest.NewRequest("GET", "/item", nil)
	secure := httptest.NewRequest("GET", "/item", nil)
	secure.TLS = &tls.ConnectionState{}
	if GetCacheKey(plain) == GetCacheKey(secure) {
		t.Fatal("TLS origin-form request shares plain HTTP identity")
	}
}
