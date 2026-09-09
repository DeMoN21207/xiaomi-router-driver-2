package subscription

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"xiomi-router-driver/internal/config"
)

func TestLoadCachedEntriesUsesStaleEndpointsWithoutNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	defer server.Close()
	dir := t.TempDir()
	if err := saveEntriesCache(entriesCachePath(dir, server.URL), server.URL, "vless://test-id@example.com:443?security=tls#Backup", time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	entries, err := LoadCachedEntries(server.URL, dir)
	if err != nil || len(entries) != 1 || entries[0].Name != "Backup" {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	if calls.Load() != 0 {
		t.Fatal("recovery contacted subscription service")
	}
	if _, err := LoadCachedEntries(server.URL, t.TempDir()); err == nil {
		t.Fatal("missing cache must report error")
	}
	if calls.Load() != 0 {
		t.Fatal("cache miss contacted subscription service")
	}
	manager := &Manager{runtimeDir: dir}
	state := config.DefaultState()
	state.Providers = []config.Provider{{ID: "p", Name: "VPN", Type: config.ProviderTypeSubscription, Source: server.URL, Enabled: true}}
	rules := []config.Rule{{ID: "r", ProviderID: "p", SelectedLocation: "Backup", Domains: []string{"example.com"}, Enabled: true}}
	desired, err := manager.buildDesired(state, rules, true)
	if err != nil || len(desired) != 1 {
		t.Fatalf("recovery build desired=%+v err=%v", desired, err)
	}
	if calls.Load() != 0 {
		t.Fatal("recovery apply contacted subscription service")
	}
}

func TestRuntimeInitializationPreservesSubscriptionCache(t *testing.T) {
	dir := t.TempDir()
	manager := NewManager(dir, dir, openSubscriptionTestDB(t), nil, nil)
	source := "https://example.com/subscription"
	if err := saveEntriesCache(entriesCachePath(manager.runtimeDir, source), source, "vless://test-id@example.com:443?security=tls#Backup", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := manager.ensureReadyLocked(); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCachedEntries(source, manager.runtimeDir); err != nil {
		t.Fatalf("runtime cleanup removed recovery cache: %v", err)
	}
	if err := manager.pruneRuntimeFilesLocked(); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCachedEntries(source, manager.runtimeDir); err != nil {
		t.Fatalf("apply cleanup removed recovery cache: %v", err)
	}
}
