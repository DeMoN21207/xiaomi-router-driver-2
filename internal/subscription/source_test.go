package subscription

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseLineVMess(t *testing.T) {
	payload, err := json.Marshal(map[string]any{
		"v":    "2",
		"ps":   "Netherlands",
		"add":  "nl.example.com",
		"port": "443",
		"id":   "11111111-1111-1111-1111-111111111111",
		"aid":  "0",
		"scy":  "auto",
		"net":  "ws",
		"host": "cdn.example.com",
		"path": "/ws",
		"tls":  "tls",
		"sni":  "sni.example.com",
	})
	if err != nil {
		t.Fatalf("marshal vmess payload: %v", err)
	}

	entry, err := ParseLine("vmess://" + base64.StdEncoding.EncodeToString(payload))
	if err != nil {
		t.Fatalf("ParseLine returned error: %v", err)
	}

	if entry.Name != "Netherlands" {
		t.Fatalf("unexpected name: %q", entry.Name)
	}
	if entry.Type != "vmess" {
		t.Fatalf("unexpected type: %q", entry.Type)
	}
	if entry.Outbound["server"] != "nl.example.com" {
		t.Fatalf("unexpected server: %#v", entry.Outbound["server"])
	}
	if entry.Outbound["network"] != "tcp" {
		t.Fatalf("unexpected network: %#v", entry.Outbound["network"])
	}
	transport, ok := entry.Outbound["transport"].(map[string]any)
	if !ok || transport["type"] != "ws" {
		t.Fatalf("unexpected transport: %#v", entry.Outbound["transport"])
	}
}

func TestParseLineVLESS(t *testing.T) {
	entry, err := ParseLine("vless://11111111-1111-1111-1111-111111111111@us.example.com:443?security=reality&type=grpc&serviceName=grpc-service&pbk=pubkey&sid=abcd&spx=%2Fprobe&sni=sni.example.com#USA")
	if err != nil {
		t.Fatalf("ParseLine returned error: %v", err)
	}

	if entry.Name != "USA" {
		t.Fatalf("unexpected name: %q", entry.Name)
	}
	if entry.Type != "vless" {
		t.Fatalf("unexpected type: %q", entry.Type)
	}
	if entry.Outbound["uuid"] != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("unexpected uuid: %#v", entry.Outbound["uuid"])
	}
	if entry.Outbound["network"] != "tcp" {
		t.Fatalf("unexpected network: %#v", entry.Outbound["network"])
	}

	tls, ok := entry.Outbound["tls"].(map[string]any)
	if !ok {
		t.Fatalf("unexpected tls: %#v", entry.Outbound["tls"])
	}
	reality, ok := tls["reality"].(map[string]any)
	if !ok || reality["public_key"] != "pubkey" {
		t.Fatalf("unexpected reality config: %#v", tls["reality"])
	}
	if _, exists := reality["spider_x"]; exists {
		t.Fatalf("unexpected deprecated reality field: %#v", reality["spider_x"])
	}
}

func TestParseLineTrojan(t *testing.T) {
	entry, err := ParseLine("trojan://secret@de.example.com:443?type=ws&host=cdn.example.com&path=%2Ftrojan&sni=sni.example.com#Germany")
	if err != nil {
		t.Fatalf("ParseLine returned error: %v", err)
	}

	if entry.Type != "trojan" {
		t.Fatalf("unexpected type: %q", entry.Type)
	}
	if entry.Outbound["password"] != "secret" {
		t.Fatalf("unexpected password: %#v", entry.Outbound["password"])
	}
}

func TestParseLineShadowsocks(t *testing.T) {
	encoded := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:secret"))
	entry, err := ParseLine("ss://" + encoded + "@jp.example.com:8388#Japan")
	if err != nil {
		t.Fatalf("ParseLine returned error: %v", err)
	}

	if entry.Type != "shadowsocks" {
		t.Fatalf("unexpected type: %q", entry.Type)
	}
	if entry.Outbound["method"] != "aes-128-gcm" {
		t.Fatalf("unexpected method: %#v", entry.Outbound["method"])
	}
	if entry.Outbound["password"] != "secret" {
		t.Fatalf("unexpected password: %#v", entry.Outbound["password"])
	}
}

func TestFetchEntriesCachedUsesFreshCacheWithoutNetwork(t *testing.T) {
	t.Setenv("VPN_MANAGER_SUBSCRIPTION_CACHE_TTL", "10m")

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "network should not be used", http.StatusInternalServerError)
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	raw := testSubscriptionPayload(t, "Japan")
	if err := saveEntriesCache(entriesCachePath(cacheDir, server.URL), server.URL, raw, time.Now().UTC()); err != nil {
		t.Fatalf("saveEntriesCache() error = %v", err)
	}

	entries, mode, err := FetchEntriesCached(server.URL, cacheDir)
	if err != nil {
		t.Fatalf("FetchEntriesCached() error = %v", err)
	}
	if mode != entriesFetchFreshCache {
		t.Fatalf("FetchEntriesCached() mode = %q, want %q", mode, entriesFetchFreshCache)
	}
	if hits.Load() != 0 {
		t.Fatalf("expected no network hits, got %d", hits.Load())
	}
	if len(entries) != 1 || entries[0].Name != "Japan" {
		t.Fatalf("unexpected cached entries: %+v", entries)
	}
}

func TestFetchEntriesCachedFallsBackToStaleCacheWhenLiveFetchFails(t *testing.T) {
	t.Setenv("VPN_MANAGER_SUBSCRIPTION_CACHE_TTL", "1s")

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "upstream down", http.StatusBadGateway)
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	raw := testSubscriptionPayload(t, "Netherlands")
	if err := saveEntriesCache(entriesCachePath(cacheDir, server.URL), server.URL, raw, time.Now().UTC().Add(-10*time.Minute)); err != nil {
		t.Fatalf("saveEntriesCache() error = %v", err)
	}

	entries, mode, err := FetchEntriesCached(server.URL, cacheDir)
	if err != nil {
		t.Fatalf("FetchEntriesCached() error = %v", err)
	}
	if mode != entriesFetchStaleCacheFallback {
		t.Fatalf("FetchEntriesCached() mode = %q, want %q", mode, entriesFetchStaleCacheFallback)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected one live fetch attempt before fallback, got %d", hits.Load())
	}
	if len(entries) != 1 || entries[0].Name != "Netherlands" {
		t.Fatalf("unexpected fallback entries: %+v", entries)
	}
}

func TestFetchEntriesCachedWritesAndReusesCache(t *testing.T) {
	t.Setenv("VPN_MANAGER_SUBSCRIPTION_CACHE_TTL", "10m")

	var hits atomic.Int32
	raw := testSubscriptionPayload(t, "Germany")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(raw))
	}))
	defer server.Close()

	cacheDir := t.TempDir()

	entries, mode, err := FetchEntriesCached(server.URL, cacheDir)
	if err != nil {
		t.Fatalf("FetchEntriesCached() first call error = %v", err)
	}
	if mode != entriesFetchLive {
		t.Fatalf("FetchEntriesCached() first mode = %q, want %q", mode, entriesFetchLive)
	}
	if len(entries) != 1 || entries[0].Name != "Germany" {
		t.Fatalf("unexpected live entries: %+v", entries)
	}

	cachePath := entriesCachePath(cacheDir, server.URL)
	if _, err := os.Stat(cachePath); err != nil {
		t.Fatalf("expected cache file to be created, err=%v", err)
	}

	server.Close()

	entries, mode, err = FetchEntriesCached(server.URL, cacheDir)
	if err != nil {
		t.Fatalf("FetchEntriesCached() cached call error = %v", err)
	}
	if mode != entriesFetchFreshCache {
		t.Fatalf("FetchEntriesCached() cached mode = %q, want %q", mode, entriesFetchFreshCache)
	}
	if hits.Load() != 1 {
		t.Fatalf("expected only one live fetch, got %d", hits.Load())
	}
	if len(entries) != 1 || entries[0].Name != "Germany" {
		t.Fatalf("unexpected reused cache entries: %+v", entries)
	}
}

func testSubscriptionPayload(t *testing.T, name string) string {
	t.Helper()

	encoded := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:secret"))
	return "ss://" + encoded + "@jp.example.com:8388#" + name + "\n"
}
