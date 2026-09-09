package automation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/runtimebin"
	"xiomi-router-driver/internal/subscription"
)

type cachedProviderProbe struct {
	result  providerProbeResult
	checked time.Time
}

func standbyProbeKey(provider config.Provider, location string) string {
	return fmt.Sprintf("standby:%x:%s", sha256.Sum256([]byte(provider.Source)), strings.ToLower(strings.TrimSpace(location)))
}

func (s *Supervisor) cachedProviderProbe(key string, maxAge time.Duration) (providerProbeResult, bool) {
	s.probeCacheMu.Lock()
	defer s.probeCacheMu.Unlock()
	cached, ok := s.probeCache[key]
	return cached.result, ok && time.Since(cached.checked) < maxAge
}

func (s *Supervisor) cacheProviderProbe(key string, result providerProbeResult) {
	s.probeCacheMu.Lock()
	defer s.probeCacheMu.Unlock()
	if s.probeCache == nil {
		s.probeCache = make(map[string]cachedProviderProbe)
	}
	for key, cached := range s.probeCache {
		if time.Since(cached.checked) > time.Minute {
			delete(s.probeCache, key)
		}
	}
	s.probeCache[key] = cachedProviderProbe{result: result, checked: time.Now()}
}

func (s *Supervisor) probeSubscriptionStandby(ctx context.Context, provider config.Provider, location string) providerProbeResult {
	key := standbyProbeKey(provider, location)
	if result, ok := s.cachedProviderProbe(key, 15*time.Second); ok {
		return result
	}
	result := s.checkSubscriptionStandby(ctx, provider, location)
	result.Location = location
	s.cacheProviderProbe(key, result)
	return result
}

func (s *Supervisor) checkSubscriptionStandby(ctx context.Context, provider config.Provider, location string) providerProbeResult {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	entries, err := subscription.LoadCachedEntries(provider.Source, s.subscriptionRuntimeDir())
	if err != nil {
		return providerProbeResult{Detail: "could not load subscription for standby probe"}
	}
	entry, found := findSubscriptionEntry(entries, location)
	if !found {
		return providerProbeResult{Detail: "standby location is missing from subscription"}
	}
	if issue := subscription.EntryEndpointIssue(entry); issue != "" {
		return providerProbeResult{Detail: issue}
	}
	return probeStandbyEntry(ctx, runtimebin.Resolve(os.Getenv("VPN_MANAGER_SINGBOX_BIN"), "sing-box", filepath.Dir(s.dataDir), s.dataDir), entry)
}

// A short-lived loopback SOCKS listener verifies the actual outbound protocol
// without installing a TUN device, touching routes or stopping the active VPN.
func probeStandbyEntry(ctx context.Context, binary string, entry subscription.Entry) providerProbeResult {
	dir, err := os.MkdirTemp("", "vpn-standby-probe-")
	if err != nil {
		return providerProbeResult{Detail: "create standby probe directory failed"}
	}
	defer os.RemoveAll(dir)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return providerProbeResult{Detail: "allocate standby probe port failed"}
	}
	address := listener.Addr().String()
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	outbound := make(map[string]any, len(entry.Outbound)+1)
	for key, value := range entry.Outbound {
		outbound[key] = value
	}
	outbound["tag"] = "proxy"
	settings := map[string]any{
		"log":       map[string]any{"disabled": true},
		"inbounds":  []any{map[string]any{"type": "socks", "listen": "127.0.0.1", "listen_port": port}},
		"outbounds": []any{outbound},
		"route":     map[string]any{"final": "proxy", "auto_detect_interface": true},
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return providerProbeResult{Detail: "encode standby probe config failed"}
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return providerProbeResult{Detail: "write standby probe config failed"}
	}
	cmd := exec.CommandContext(ctx, binary, "run", "-c", path)
	if err := cmd.Start(); err != nil {
		return providerProbeResult{Detail: "start standby probe failed"}
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	defer func() { _ = cmd.Process.Kill(); <-done }()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return providerProbeResult{Detail: "standby probe startup timed out"}
		case <-done:
			return providerProbeResult{Detail: "standby probe process exited"}
		case <-ticker.C:
			conn, dialErr := net.DialTimeout("tcp4", address, 50*time.Millisecond)
			if dialErr != nil {
				continue
			}
			conn.Close()
			transport := &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "socks5", Host: address}), DisableKeepAlives: true,
				TLSHandshakeTimeout: providerProbeTimeout, ResponseHeaderTimeout: providerProbeTimeout}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			return probeTunnelURLs(ctx, client, []string{"https://1.1.1.1/", "https://8.8.8.8/"})
		}
	}
}
