package subscription

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultSubscriptionEntriesCacheTTL = time.Minute

type entriesFetchMode string

const (
	entriesFetchLive               entriesFetchMode = "live"
	entriesFetchFreshCache         entriesFetchMode = "cache_fresh"
	entriesFetchStaleCacheFallback entriesFetchMode = "cache_stale_fallback"
)

type entriesCacheSnapshot struct {
	Source    string `json:"source"`
	FetchedAt string `json:"fetchedAt"`
	Raw       string `json:"raw"`
}

type subscriptionSource struct {
	CacheKey string
	FetchURL string
	Inline   string
}

type subscriptionFetchProfile struct {
	UserAgent     string
	Accept        string
	DeviceHeaders bool
}

func FetchEntriesCached(source string, runtimeDir string) ([]Entry, entriesFetchMode, error) {
	normalizedSource, err := normalizeSubscriptionSource(source)
	if err != nil {
		return nil, entriesFetchLive, err
	}

	source = normalizedSource.CacheKey
	cachePath := entriesCachePath(runtimeDir, source)
	cacheTTL := subscriptionEntriesCacheTTL()

	if cacheTTL > 0 {
		if snapshot, ok := loadEntriesCache(cachePath, source); ok && snapshot.isFresh(cacheTTL) {
			entries, err := ParseEntries(snapshot.Raw)
			if err == nil && entriesLookUsable(entries) {
				return entries, entriesFetchFreshCache, nil
			}
		}
	}

	entries, raw, err := fetchEntriesLive(normalizedSource, false)
	if err == nil {
		if cachePath != "" {
			_ = saveEntriesCache(cachePath, source, raw, time.Now().UTC())
		}
		return entries, entriesFetchLive, nil
	}

	if snapshot, ok := loadEntriesCache(cachePath, source); ok {
		entries, parseErr := ParseEntries(snapshot.Raw)
		if parseErr == nil && entriesLookUsable(entries) {
			return entries, entriesFetchStaleCacheFallback, nil
		}
	}

	return nil, entriesFetchLive, err
}

func RefreshEntriesCached(source string, runtimeDir string) ([]Entry, error) {
	normalizedSource, err := normalizeSubscriptionSource(source)
	if err != nil {
		return nil, err
	}

	entries, raw, err := fetchEntriesLive(normalizedSource, true)
	if err != nil {
		return nil, err
	}

	cachePath := entriesCachePath(runtimeDir, normalizedSource.CacheKey)
	if cachePath != "" {
		if err := saveEntriesCache(cachePath, normalizedSource.CacheKey, raw, time.Now().UTC()); err != nil {
			return nil, fmt.Errorf("save subscription cache: %w", err)
		}
	}
	return entries, nil
}

func entriesCachePath(runtimeDir string, source string) string {
	runtimeDir = strings.TrimSpace(runtimeDir)
	source = strings.TrimSpace(source)
	if runtimeDir == "" || source == "" {
		return ""
	}
	return filepath.Join(runtimeDir, "source-cache-"+shortHash(source)+".json")
}

func subscriptionEntriesCacheTTL() time.Duration {
	raw := strings.TrimSpace(os.Getenv("VPN_MANAGER_SUBSCRIPTION_CACHE_TTL"))
	if raw == "" {
		return defaultSubscriptionEntriesCacheTTL
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < 0 {
		return defaultSubscriptionEntriesCacheTTL
	}
	return parsed
}

func loadEntriesCache(path string, source string) (entriesCacheSnapshot, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return entriesCacheSnapshot{}, false
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return entriesCacheSnapshot{}, false
	}

	var snapshot entriesCacheSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return entriesCacheSnapshot{}, false
	}
	if strings.TrimSpace(snapshot.Source) != strings.TrimSpace(source) || strings.TrimSpace(snapshot.Raw) == "" {
		return entriesCacheSnapshot{}, false
	}

	return snapshot, true
}

func saveEntriesCache(path string, source string, raw string, fetchedAt time.Time) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	payload, err := json.Marshal(entriesCacheSnapshot{
		Source:    strings.TrimSpace(source),
		FetchedAt: fetchedAt.UTC().Format(time.RFC3339),
		Raw:       raw,
	})
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	return os.WriteFile(path, payload, 0o600)
}

func (s entriesCacheSnapshot) isFresh(ttl time.Duration) bool {
	if ttl <= 0 {
		return false
	}

	fetchedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(s.FetchedAt))
	if err != nil {
		return false
	}
	age := time.Since(fetchedAt.UTC())
	if age < 0 {
		return false
	}
	return age <= ttl
}

func fetchEntriesRaw(source string) (string, error) {
	normalized, err := normalizeSubscriptionSource(source)
	if err != nil {
		return "", err
	}
	if normalized.Inline != "" {
		return normalized.Inline, nil
	}

	return fetchEntriesRawWithProfile(normalized.FetchURL, subscriptionFetchProfile{})
}

func fetchEntriesLive(source subscriptionSource, retryFetchErrors bool) ([]Entry, string, error) {
	if source.Inline != "" {
		entries, err := ParseEntries(source.Inline)
		if err != nil {
			return nil, "", err
		}
		return entries, source.Inline, nil
	}

	profiles := subscriptionFetchProfiles()
	var lastErr error
	for _, profile := range profiles {
		raw, err := fetchEntriesRawWithProfile(source.FetchURL, profile)
		if err != nil {
			lastErr = err
			if !retryFetchErrors {
				break
			}
			continue
		}
		entries, err := ParseEntries(raw)
		if err == nil && entriesLookUsable(entries) {
			return entries, raw, nil
		}
		if err == nil {
			err = errors.New("subscription returned only compatibility placeholder entries")
		}
		lastErr = err
	}

	if lastErr == nil {
		lastErr = errors.New("subscription fetch profiles are not configured")
	}
	return nil, "", lastErr
}

func fetchEntriesRawWithProfile(source string, profile subscriptionFetchProfile) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, source, nil)
	if err != nil {
		return "", fmt.Errorf("prepare subscription request: %w", err)
	}
	if strings.TrimSpace(profile.UserAgent) != "" {
		req.Header.Set("User-Agent", profile.UserAgent)
	}
	if strings.TrimSpace(profile.Accept) != "" {
		req.Header.Set("Accept", profile.Accept)
	}
	if profile.DeviceHeaders {
		hwid := "xiaomi-router-driver-" + shortHash(source)[:16]
		req.Header.Set("X-HWID", hwid)
		req.Header.Set("X-Device-OS", "OpenWrt")
		req.Header.Set("X-Device-Model", "Xiaomi Router")
		req.Header.Set("X-App-Version", "1.0.0")
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("load subscription: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("subscription server returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSubscriptionBodySize))
	if err != nil {
		return "", fmt.Errorf("read subscription response: %w", err)
	}

	return string(body), nil
}

func validateSubscriptionSource(source string) (string, error) {
	normalized, err := normalizeSubscriptionSource(source)
	if err != nil {
		return "", err
	}
	return normalized.CacheKey, nil
}

func normalizeSubscriptionSource(source string) (subscriptionSource, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return subscriptionSource{}, errors.New("subscription URL is empty")
	}

	source = unwrapSubscriptionImportURL(source)
	if looksLikeInlineSubscription(source) {
		return subscriptionSource{
			CacheKey: source,
			Inline:   source,
		}, nil
	}

	parsed, err := url.Parse(source)
	if err != nil {
		return subscriptionSource{}, fmt.Errorf("invalid subscription URL: %w", err)
	}
	if strings.TrimSpace(parsed.Scheme) == "" || strings.TrimSpace(parsed.Host) == "" {
		return subscriptionSource{}, errors.New("subscription URL must include scheme and host")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return subscriptionSource{}, fmt.Errorf("unsupported subscription URL scheme: %s", parsed.Scheme)
	}

	fetchURL := *parsed
	fetchURL.Fragment = ""
	return subscriptionSource{
		CacheKey: source,
		FetchURL: fetchURL.String(),
	}, nil
}

func subscriptionFetchProfiles() []subscriptionFetchProfile {
	return []subscriptionFetchProfile{
		{},
		{
			UserAgent:     "Happ",
			Accept:        "application/json, text/plain, */*",
			DeviceHeaders: true,
		},
		{
			UserAgent:     "v2raytun/ios",
			Accept:        "application/json, text/plain, */*",
			DeviceHeaders: true,
		},
		{
			UserAgent:     "v2RayTun/5.22.69",
			Accept:        "application/json, text/plain, */*",
			DeviceHeaders: true,
		},
		{
			UserAgent:     "Happ/1.0",
			Accept:        "application/json, text/plain, */*",
			DeviceHeaders: true,
		},
		{
			UserAgent: "HiddifyNext/2.0",
			Accept:    "application/json, text/plain, */*",
		},
		{
			UserAgent: "sing-box/1.11",
			Accept:    "application/json, text/plain, */*",
		},
		{
			UserAgent: "v2rayN/7.0",
			Accept:    "text/plain, application/json, */*",
		},
		{
			UserAgent:     "clash-meta",
			Accept:        "text/yaml, application/yaml, text/plain, */*",
			DeviceHeaders: true,
		},
	}
}

func entriesLookUsable(entries []Entry) bool {
	if len(entries) == 0 {
		return false
	}
	for _, entry := range entries {
		if !isCompatibilityPlaceholderEntry(entry) {
			return true
		}
	}
	return false
}

func isCompatibilityPlaceholderEntry(entry Entry) bool {
	address := strings.TrimSpace(entry.Address)
	if address != "0.0.0.0:1" && address != "[::]:1" {
		return false
	}
	return true
}

func looksLikeInlineSubscription(source string) bool {
	source = strings.TrimSpace(source)
	if source == "" {
		return false
	}
	if strings.Contains(source, "\n") {
		return true
	}
	for _, prefix := range []string{"vmess://", "vless://", "trojan://", "ss://"} {
		if strings.HasPrefix(source, prefix) {
			return true
		}
	}
	return false
}

func unwrapSubscriptionImportURL(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return source
	}

	for {
		next := unwrapSubscriptionImportURLOnce(source)
		if next == source {
			return source
		}
		source = next
	}
}

func unwrapSubscriptionImportURLOnce(source string) string {
	lower := strings.ToLower(source)
	for _, prefix := range []string{
		"hiddify://import/",
		"streisand://import/",
		"v2raytun://import/",
	} {
		if strings.HasPrefix(lower, prefix) {
			value := strings.TrimSpace(source[len(prefix):])
			if decoded, err := url.QueryUnescape(value); err == nil && strings.TrimSpace(decoded) != "" {
				return strings.TrimSpace(decoded)
			}
			return value
		}
	}

	parsed, err := url.Parse(source)
	if err != nil {
		return source
	}
	switch strings.ToLower(parsed.Scheme) {
	case "clash", "clash-meta", "hiddify", "sing-box", "streisand", "v2raytun":
	default:
		return source
	}
	for _, key := range []string{"url", "sub", "subscription"} {
		value := strings.TrimSpace(parsed.Query().Get(key))
		if value == "" {
			continue
		}
		if decoded, err := url.QueryUnescape(value); err == nil && strings.TrimSpace(decoded) != "" {
			return strings.TrimSpace(decoded)
		}
		return value
	}
	return source
}
