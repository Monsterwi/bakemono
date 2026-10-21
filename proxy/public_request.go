package proxy

import (
	"context"
	"net/http"
)

// PublicRequestVersion changes whenever canonical producer semantics change.
const PublicRequestVersion = "public-static-v1"

type publicRequestKey struct{}

// NormalizePublicRequest is only for admitted public cache candidates. Bypass
// requests must retain their original forwarding and authentication semantics.
func (h *HTTPProxy) NormalizePublicRequest(r *http.Request) *http.Request {
	_, _, scheme := h.forwarding(r)
	out := r.Clone(context.WithValue(r.Context(), publicRequestKey{}, scheme))
	stripHopHeaders(out.Header)
	for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Real-IP", "Traceparent", "Tracestate", "Baggage", "X-Request-Id", "X-Correlation-Id"} {
		out.Header.Del(name)
	}
	out.Header.Set("User-Agent", "Razor")
	out.Header.Set("X-Forwarded-Host", r.Host)
	out.Header.Set("X-Forwarded-Proto", scheme)
	// Detached producer identity is the canonical request, never the first peer.
	out.RemoteAddr = ""
	out.TLS = nil
	return out
}

// CloneRequest preserves canonical forwarding across detached producers;
// cancellation and deadlines still belong entirely to the supplied context.
func CloneRequest(r *http.Request, ctx context.Context) *http.Request {
	if scheme, ok := r.Context().Value(publicRequestKey{}).(string); ok {
		ctx = context.WithValue(ctx, publicRequestKey{}, scheme)
	}
	return r.Clone(ctx)
}
