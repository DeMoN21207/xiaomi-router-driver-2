package probe

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"xiomi-router-driver/internal/subscription"
)

func TestProbeSubscriptionUsesCachedEntries(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) > 1 {
			http.Error(w, "live source is temporarily slow", http.StatusGatewayTimeout)
			return
		}
		_, _ = w.Write([]byte(testProbeSubscriptionPayload("Cached")))
	}))
	defer server.Close()

	baseDir := t.TempDir()
	cacheDir := filepath.Join(baseDir, ".vpn-manager", "subscriptions")
	if _, err := subscription.RefreshEntriesCached(server.URL, cacheDir); err != nil {
		t.Fatalf("RefreshEntriesCached() error = %v", err)
	}

	result := ProbeSource("subscription", server.URL, baseDir)
	if result.Error != "" {
		t.Fatalf("ProbeSource() error = %q, want cached locations", result.Error)
	}
	if hits.Load() != 1 {
		t.Fatalf("ProbeSource() hit live source, hits = %d; want cached-only probe", hits.Load())
	}
	if len(result.Locations) != 1 || result.Locations[0].Name != "Cached" {
		t.Fatalf("unexpected locations: %+v", result.Locations)
	}
}

func testProbeSubscriptionPayload(name string) string {
	encoded := base64.RawURLEncoding.EncodeToString([]byte("aes-128-gcm:secret"))
	return "ss://" + encoded + "@jp.example.com:8388#" + name + "\n"
}
