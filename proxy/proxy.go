package proxy

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Monsterwi/razor/balancer"
	"github.com/Monsterwi/razor/logger"
	"github.com/go-resty/resty/v2"
)

var (
	XRealIP       = http.CanonicalHeaderKey("X-Real-IP")
	XProxy        = http.CanonicalHeaderKey("X-Proxy")
	XForwardedFor = http.CanonicalHeaderKey("X-Forwarded-For")
	ReverseProxy  = "Balancer-Reverse-Proxy"
)

var transport = &http.Transport{
	MaxIdleConnsPerHost: 256,
	IdleConnTimeout:     90 * time.Second,
	DisableCompression:  true,
}

// HTTPProxy shares a connection pool across upstream requests.
type HTTPProxy struct {
	trustedProxies []netip.Prefix
	targets        map[string]*url.URL
	lb             balancer.Balancer
	transport      http.RoundTripper
	sync.RWMutex
	alive map[string]bool
}

// Option configures a proxy before it starts serving requests.
type Option func(*HTTPProxy) error

// WithTransport supplies a shared transport, for example with private CA roots.
// The caller owns its connection pool and must disable transparent decompression.
func WithTransport(transport http.RoundTripper) Option {
	return func(h *HTTPProxy) error {
		if transport == nil {
			return fmt.Errorf("upstream transport is nil")
		}
		h.transport = transport
		return nil
	}
}

// WithTrustedProxies accepts forwarded identity only from these CIDR networks.
func WithTrustedProxies(networks []string) Option {
	return func(h *HTTPProxy) error {
		for _, network := range networks {
			prefix, err := netip.ParsePrefix(network)
			if err != nil {
				return fmt.Errorf("trusted proxy %q: %w", network, err)
			}
			h.trustedProxies = append(h.trustedProxies, prefix)
		}
		return nil
	}
}

func (h *HTTPProxy) trusted(ip netip.Addr) bool {
	for _, prefix := range h.trustedProxies {
		if prefix.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

func (h *HTTPProxy) forwarding(r *http.Request) (chain, clientIP, scheme string) {
	clientIP = GetIP(r)
	chain = clientIP
	scheme = "http"
	if r.TLS != nil {
		scheme = "https"
	}
	peer, err := netip.ParseAddr(clientIP)
	if err != nil || !h.trusted(peer) {
		return
	}
	headers := r.Header.Clone()
	stripHopHeaders(headers)
	if proto := headers.Get("X-Forwarded-Proto"); proto == "http" || proto == "https" {
		scheme = proto
	}
	// Walk from the trusted peer toward the client; ignore any untrusted prefix.
	values := strings.Split(strings.Join(headers.Values(XForwardedFor), ","), ",")
	for i := len(values) - 1; i >= 0; i-- {
		ip, err := netip.ParseAddr(strings.TrimSpace(values[i]))
		if err != nil {
			break
		}
		clientIP = ip.Unmap().String()
		chain = clientIP + ", " + chain
		if !h.trusted(ip) {
			break
		}
	}
	return
}

// NewHTTPProxy preserves each upstream's scheme, authority, base path and query.
func NewHTTPProxy(targetHosts []string, algorithm string, options ...Option) (*HTTPProxy, error) {
	h := &HTTPProxy{targets: make(map[string]*url.URL), alive: make(map[string]bool), transport: transport}
	for _, option := range options {
		if err := option(h); err != nil {
			return nil, err
		}
	}
	hosts := make([]string, 0, len(targetHosts))
	for _, targetHost := range targetHosts {
		target, err := url.Parse(targetHost)
		if err != nil {
			return nil, err
		}
		if (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" || target.User != nil || target.Fragment != "" {
			return nil, fmt.Errorf("invalid upstream URL %q", targetHost)
		}
		key := target.String()
		if _, exists := h.targets[key]; exists {
			continue
		}
		h.targets[key] = target
		h.alive[key] = true
		hosts = append(hosts, key)
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("at least one upstream is required")
	}
	var err error
	h.lb, err = balancer.Build(algorithm, hosts)
	if err != nil {
		return nil, err
	}
	return h, nil
}

func (h *HTTPProxy) rewrite(p *httputil.ProxyRequest, target *url.URL) {
	stripHopHeaders(p.Out.Header)
	// Cache identity and the origin must see the same query bytes.
	p.Out.URL.RawQuery = p.In.URL.RawQuery
	p.SetURL(target)
	for _, name := range []string{"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-IP"} {
		p.Out.Header.Del(name)
	}
	chain, ip, scheme := h.forwarding(p.In)
	if ip != "" {
		p.Out.Header.Set(XForwardedFor, chain)
		p.Out.Header.Set(XRealIP, ip)
	}
	p.Out.Header.Set(XProxy, ReverseProxy)
	p.Out.Header.Set("X-Forwarded-Host", p.In.Host)
	p.Out.Header.Set("X-Forwarded-Proto", scheme)
	p.Out.RequestURI = ""
	p.Out.Close = false
}

// CacheKey uses the same trusted scheme as the forwarded upstream request.
func (h *HTTPProxy) CacheKey(r *http.Request) string {
	_, _, scheme := h.forwarding(r)
	copy := *r
	target := *r.URL
	target.Scheme = scheme
	copy.URL = &target
	return GetCacheKey(&copy)
}

// ServeHTTP and Proxy use the same request construction and upstream transport.
func (h *HTTPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, err := h.lb.Balance(h.CacheKey(r))
	if err != nil {
		http.Error(w, "no available upstream", http.StatusBadGateway)
		return
	}
	h.lb.Inc(host)
	defer h.lb.Done(host)
	p := &httputil.ReverseProxy{
		Rewrite:        func(p *httputil.ProxyRequest) { h.rewrite(p, h.targets[host]) },
		Transport:      h.transport,
		FlushInterval:  -1,
		ModifyResponse: func(r *http.Response) error { stripHopHeaders(r.Header); return nil },
	}
	p.ServeHTTP(w, r)
}

// Proxy returns an unbuffered response. The caller must close RawBody.
func (h *HTTPProxy) Proxy(r *http.Request) (*resty.Response, error) {
	host, err := h.lb.Balance(h.CacheKey(r))
	if err != nil {
		return nil, fmt.Errorf("balance: %w", err)
	}
	h.lb.Inc(host)
	out := r.Clone(r.Context())
	h.rewrite(&httputil.ProxyRequest{In: r, Out: out}, h.targets[host])
	resp, err := h.transport.RoundTrip(out)
	if err != nil {
		h.lb.Done(host)
		return nil, err
	}
	stripHopHeaders(resp.Header)
	resp.Body = &trackedBody{ReadCloser: resp.Body, done: func() { h.lb.Done(host) }}
	return &resty.Response{RawResponse: resp}, nil
}

type trackedBody struct {
	io.ReadCloser
	once sync.Once
	done func()
}

func (b *trackedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(b.done)
	}
	return n, err
}

func (b *trackedBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(b.done)
	return err
}

// health check start
// ReadAlive reads the alive status of the site
func (h *HTTPProxy) ReadAlive(url string) bool {
	h.RLock()
	defer h.RUnlock()
	return h.alive[url]
}

// SetAlive sets the alive status to the site
func (h *HTTPProxy) SetAlive(url string, alive bool) {
	h.Lock()
	defer h.Unlock()
	h.alive[url] = alive
}

// HealthCheck enable a health check goroutine for each agent
func (h *HTTPProxy) HealthCheck(interval uint) {
	for host := range h.targets {
		go h.healthCheck(host, interval)
	}
}

// healthCheck goroutine
func (h *HTTPProxy) healthCheck(host string, interval uint) {
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	for range ticker.C {
		if !IsBackendAlive(GetHost(h.targets[host])) && h.ReadAlive(host) {
			logger.Infof("Site unreachable, remove %s from load balancer.", host)
			h.SetAlive(host, false)
			h.lb.Remove(host)
		} else if IsBackendAlive(GetHost(h.targets[host])) && !h.ReadAlive(host) {
			logger.Infof("Site reachable, add %s to load balancer.", host)
			h.SetAlive(host, true)
			h.lb.Add(host)
		}
	}

}

// health check end

// ConnectionTimeout refers to connection timeout for health check
var ConnectionTimeout = 3 * time.Second

// GetIP get client IP
func GetIP(r *http.Request) string {
	clientIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return ""
	}
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return ""
	}
	return ip.String()
}

func stripHopHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			header.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		header.Del(name)
	}
}

// GetHost get the hostname, looks like IP:Port
func GetHost(url *url.URL) string {
	if _, _, err := net.SplitHostPort(url.Host); err == nil {
		return url.Host
	}
	if url.Scheme == "http" {
		return fmt.Sprintf("%s:%s", url.Host, "80")
	} else if url.Scheme == "https" {
		return fmt.Sprintf("%s:%s", url.Host, "443")
	}
	return url.Host
}

// IsBackendAlive Attempt to establish a tcp connection to determine whether the site is alive
func IsBackendAlive(host string) bool {
	addr, err := net.ResolveTCPAddr("tcp", host)
	if err != nil {
		return false
	}
	conn, err := net.DialTimeout("tcp", addr.String(), ConnectionTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
