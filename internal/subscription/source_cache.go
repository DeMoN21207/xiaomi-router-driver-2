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

func FetchEntriesCached(source string, runtimeDir string) ([]Entry, entriesFetchMode, error) {
	normalizedSource, err := validateSubscriptionSource(source)
	if err != nil {
		return nil, entriesFetchLive, err
	}

	source = normalizedSource
	cachePath := entriesCachePath(runtimeDir, source)
	cacheTTL := subscriptionEntriesCacheTTL()

	if cacheTTL > 0 {
		if snapshot, ok := loadEntriesCache(cachePath, source); ok && snapshot.isFresh(cacheTTL) {
			entries, err := ParseEntries(snapshot.Raw)
			if err == nil {
				return entries, entriesFetchFreshCache, nil
			}
		}
	}

	raw, err := fetchEntriesRaw(source)
	if err == nil {
		entries, parseErr := ParseEntries(raw)
		if parseErr != nil {
			return nil, entriesFetchLive, parseErr
		}
		if cachePath != "" {
			_ = saveEntriesCache(cachePath, source, raw, time.Now().UTC())
		}
		return entries, entriesFetchLive, nil
	}

	if snapshot, ok := loadEntriesCache(cachePath, source); ok {
		entries, parseErr := ParseEntries(snapshot.Raw)
		if parseErr == nil {
			return entries, entriesFetchStaleCacheFallback, nil
		}
	}

	return nil, entriesFetchLive, err
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
	if _, err := validateSubscriptionSource(source); err != nil {
		return "", err
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(source)
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
	source = strings.TrimSpace(source)
	if source == "" {
		return "", errors.New("subscription URL is empty")
	}
	if _, err := url.ParseRequestURI(source); err != nil {
		return "", fmt.Errorf("invalid subscription URL: %w", err)
	}
	return source, nil
}
