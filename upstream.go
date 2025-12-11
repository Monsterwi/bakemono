package main

// func NewUpstream(addr string) *Upstream {
// 	return &Upstream{addr: addr}
// }

// func (u *Upstream) Upstream(c *gin.Context) (io.ReadCloser, error) {
// 	client := resty.New().SetDoNotParseResponse(true)
// 	// SetTimeout(3 * time.Second)
// 	// client.SetTransport(&http.Transport{
// 	// 	MaxIdleConnsPerHost: 500,
// 	// 	MaxConnsPerHost:     0,
// 	// 	IdleConnTimeout:     time.Minute * 5,
// 	// })
// 	addr := "http://" + u.addr + "/" + c.Request.URL.Path
// 	resp, err := client.R().Get(addr)
// 	if err != nil {
// 		return nil, err
// 	}
// 	log.Printf("upstream resp statusCode %d", resp.StatusCode())
// 	if resp.StatusCode() == 200 {
// 		return resp.RawBody(), nil
// 	}
// 	return nil, errors.New("status code not 200")
// }
import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
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
)

var (
	ReverseProxy = "Balancer-Reverse-Proxy"
)

var transport = &http.Transport{
	MaxIdleConnsPerHost: 256,
	MaxConnsPerHost:     0,
	IdleConnTimeout:     90 * time.Second,
}

// HTTPProxy refers to a reverse proxy in the balancer
type HTTPProxy struct {
	hostMap map[string]*httputil.ReverseProxy
	lb      balancer.Balancer

	sync.RWMutex // protect alive
	alive        map[string]bool
}

// NewHTTPProxy create  new reverse proxy with url and balancer algorithm
func NewHTTPProxy(targetHosts []string, algorithm string) (
	*HTTPProxy, error) {

	hosts := make([]string, 0)
	hostMap := make(map[string]*httputil.ReverseProxy)
	alive := make(map[string]bool)
	for _, targetHost := range targetHosts {
		url, err := url.Parse(targetHost)
		if err != nil {
			return nil, err
		}
		proxy := httputil.NewSingleHostReverseProxy(url)

		originDirector := proxy.Director
		proxy.Director = func(req *http.Request) {
			originDirector(req)
			req.Header.Set(XProxy, ReverseProxy)
			req.Header.Set(XRealIP, GetIP(req))
		}

		host := GetHost(url)
		alive[host] = true // initial mark alive
		hostMap[host] = proxy
		hosts = append(hosts, host)
	}

	lb, err := balancer.Build(algorithm, hosts)
	if err != nil {
		return nil, err
	}

	return &HTTPProxy{
		hostMap: hostMap,
		lb:      lb,
		alive:   alive,
	}, nil
}

// ServeHTTP implements a proxy to the http server
func (h *HTTPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, err := h.lb.Balance(GetCacheKey(r))
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(fmt.Sprintf("balance error: %s", err.Error())))
		return
	}

	h.lb.Inc(host)
	defer h.lb.Done(host)

	h.hostMap[host].ServeHTTP(w, r)
}

func (h *HTTPProxy) Proxy(r *http.Request) (*resty.Response, error) {
	host, err := h.lb.Balance(GetCacheKey(r))
	if err != nil {
		return nil, fmt.Errorf("balance error: %s", err.Error())
	}
	// log.Printf("balance host: %s, key: %s", host, cacheKey)
	h.lb.Inc(host)
	defer h.lb.Done(host)

	proxyURL := "http://" + host + r.URL.RequestURI()

	client := resty.New().
		SetDoNotParseResponse(true).
		SetTransport(transport).
		SetTimeout(30 * time.Second)
	for k, v := range r.Header {
		for _, vv := range v {
			client.SetHeader(k, vv)
		}
	}
	client.SetHeader(XRealIP, GetIP(r))
	client.SetHeader(XProxy, ReverseProxy)
	client.SetHeader(XForwardedFor, GetIP(r))

	return client.R().Get(proxyURL)
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
	for host := range h.hostMap {
		go h.healthCheck(host, interval)
	}
}

// healthCheck goroutine
func (h *HTTPProxy) healthCheck(host string, interval uint) {
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	for range ticker.C {
		if !IsBackendAlive(host) && h.ReadAlive(host) {
			logger.Infof("Site unreachable, remove %s from load balancer.", host)
			h.SetAlive(host, false)
			h.lb.Remove(host)
		} else if IsBackendAlive(host) && !h.ReadAlive(host) {
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
	clientIP, _, _ := net.SplitHostPort(r.RemoteAddr)
	if len(r.Header.Get(XForwardedFor)) != 0 {
		xff := r.Header.Get(XForwardedFor)
		s := strings.Index(xff, ", ")
		if s == -1 {
			s = len(r.Header.Get(XForwardedFor))
		}
		clientIP = xff[:s]
	} else if len(r.Header.Get(XRealIP)) != 0 {
		clientIP = r.Header.Get(XRealIP)
	}

	return clientIP
}

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
	resolveAddr := fmt.Sprintf("%s:%d", addr.IP, addr.Port)
	conn, err := net.DialTimeout("tcp", resolveAddr, ConnectionTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
