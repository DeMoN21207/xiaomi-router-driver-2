package automation

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

func (s *Supervisor) subscriptionTunnelProbe(ctx context.Context, iface, mark string) providerProbeResult {
	if s.probeTunnel != nil {
		return s.probeTunnel(ctx, iface, mark)
	}
	key := "tunnel:" + iface + ":" + mark
	if result, ok := s.cachedProviderProbe(key, time.Second); ok {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, providerProbeTimeout)
	defer cancel()
	dialer := &net.Dialer{Control: func(_, _ string, conn syscall.RawConn) error {
		return bindTunnelProbeSocket(conn, iface, mark)
	}}
	transport := &http.Transport{DialContext: dialer.DialContext, DisableKeepAlives: true,
		TLSHandshakeTimeout: providerProbeTimeout, ResponseHeaderTimeout: providerProbeTimeout}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	// Literal IPs avoid depending on the router's DNS. TLS still verifies the
	// certificate. Either independent destination is sufficient; no direct WAN
	// fallback is allowed when binding the tunnel socket fails.
	result := probeTunnelURLs(ctx, client, []string{"https://1.1.1.1/", "https://8.8.8.8/"})
	result.Detail = fmt.Sprintf("%s via %s", result.Detail, iface)
	s.cacheProviderProbe(key, result)
	return result
}

func probeTunnelURLs(ctx context.Context, client *http.Client, urls []string) providerProbeResult {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan providerProbeResult, len(urls))
	for _, target := range urls {
		go func(target string) {
			start := time.Now()
			req, err := http.NewRequestWithContext(ctx, http.MethodHead, target, nil)
			if err != nil {
				results <- providerProbeResult{Detail: err.Error()}
				return
			}
			response, err := client.Do(req)
			if err != nil {
				results <- providerProbeResult{Detail: err.Error()}
				return
			}
			response.Body.Close()
			healthy := response.StatusCode >= 200 && response.StatusCode < 400
			results <- providerProbeResult{Healthy: healthy, LatencyMs: maxInt64(1, time.Since(start).Milliseconds()),
				Detail: fmt.Sprintf("HTTPS %s returned %d", target, response.StatusCode)}
		}(target)
	}
	errors := make([]string, 0, len(urls))
	for range urls {
		select {
		case <-ctx.Done():
			return providerProbeResult{Detail: "tunnel HTTPS probe: " + ctx.Err().Error()}
		case result := <-results:
			if result.Healthy {
				return result
			}
			errors = append(errors, result.Detail)
		}
	}
	return providerProbeResult{Detail: "tunnel HTTPS probe failed: " + strings.Join(errors, "; ")}
}
