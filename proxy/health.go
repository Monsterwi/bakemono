package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// HealthConfig uses HEAD against Path on each configured scheme and authority.
// A 2xx or 3xx response is healthy; redirects are never followed. Zero interval
// disables probing. Checks run sequentially, with one bounded request at a time.
type HealthConfig struct {
	Interval, Timeout   time.Duration
	Failures, Successes int
	Path                string
}

func (c HealthConfig) Validate() error {
	if c.Interval < 0 || c.Interval > 24*time.Hour {
		return fmt.Errorf("invalid health interval")
	}
	if c.Interval == 0 {
		return nil
	}
	if c.Timeout <= 0 || c.Timeout > time.Minute || c.Failures < 1 || c.Failures > 1000 || c.Successes < 1 || c.Successes > 1000 {
		return fmt.Errorf("invalid health timeout or thresholds")
	}
	if c.Path != "" {
		u, err := url.ParseRequestURI(c.Path)
		if err != nil || u.IsAbs() || u.Host != "" || c.Path[0] != '/' || u.Fragment != "" {
			return fmt.Errorf("health path must be an absolute request path")
		}
	}
	return nil
}

// StartHealthChecks starts once, until StopBackground or Close. It must run after
// the proxy transport and limits are configured and before serving traffic.
func (h *HTTPProxy) StartHealthChecks(c HealthConfig) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Interval == 0 {
		return nil
	}
	if c.Path == "" {
		c.Path = "/"
	}
	h.healthMu.Lock()
	defer h.healthMu.Unlock()
	if h.healthStopped {
		return fmt.Errorf("health checks already stopped")
	}
	if h.healthCancel != nil {
		return fmt.Errorf("health checks already started")
	}
	ctx, cancel := context.WithCancel(h.ctx)
	h.healthCancel = cancel
	h.healthWG.Add(1)
	go func() {
		defer h.healthWG.Done()
		h.checkHealth(ctx, c)
	}()
	return nil
}

// HealthCheck preserves the original seconds-based API with default thresholds.
func (h *HTTPProxy) HealthCheck(interval uint) {
	if interval == 0 || interval > 86400 {
		return
	}
	_ = h.StartHealthChecks(HealthConfig{Interval: time.Duration(interval) * time.Second, Timeout: ConnectionTimeout, Failures: 2, Successes: 2})
}

func (h *HTTPProxy) checkHealth(ctx context.Context, c HealthConfig) {
	type streak struct{ failures, successes int }
	counts := make(map[string]streak, len(h.targets))
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()
	for {
		for host, target := range h.targets {
			if ctx.Err() != nil {
				return
			}
			probeCtx, cancel := context.WithTimeout(ctx, c.Timeout)
			endpoint := target.ResolveReference(&url.URL{Path: "/"})
			path, _ := url.ParseRequestURI(c.Path)
			endpoint.Path, endpoint.RawPath, endpoint.RawQuery = path.Path, path.RawPath, path.RawQuery
			request, _ := http.NewRequestWithContext(probeCtx, http.MethodHead, endpoint.String(), nil)
			response, err := h.transport.RoundTrip(request)
			healthy := err == nil && response.StatusCode >= 200 && response.StatusCode < 400
			if response != nil {
				response.Body.Close()
			}
			cancel()
			if ctx.Err() != nil {
				return
			}
			count := counts[host]
			if healthy {
				count.failures = 0
				count.successes = min(count.successes+1, c.Successes)
			} else {
				count.successes = 0
				count.failures = min(count.failures+1, c.Failures)
			}
			counts[host] = count
			alive := h.ReadAlive(host)
			if alive && count.failures >= c.Failures {
				h.lb.Remove(host)
				h.SetAlive(host, false)
			}
			if !alive && count.successes >= c.Successes {
				h.lb.Add(host)
				h.SetAlive(host, true)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
