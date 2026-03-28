package subscription

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/sqlitedb"
)

func TestManagerPersistsRuntimeInstancesInSQLite(t *testing.T) {
	tempDir := t.TempDir()
	db := openSubscriptionTestDB(t)
	manager := NewManager(tempDir, tempDir, db, nil, nil)

	manager.mu.Lock()
	if err := manager.ensureReadyLocked(); err != nil {
		manager.mu.Unlock()
		t.Fatalf("ensureReadyLocked() error = %v", err)
	}
	instance := &managedInstance{
		Key:           "provider_1::nl",
		ProviderID:    "provider_1",
		ProviderName:  "FizzVPN",
		Location:      "NL",
		InterfaceName: "sbnl123456",
		DomainCount:   2,
		ConfigPath:    filepath.Join(manager.runtimeDir, "nl.json"),
		Settings:      config.DefaultRoutingSettings(),
		PID:           1234,
	}
	if err := manager.saveInstanceLocked(instance); err != nil {
		manager.mu.Unlock()
		t.Fatalf("saveInstanceLocked() error = %v", err)
	}
	manager.mu.Unlock()

	snapshots, err := manager.Snapshots()
	if err != nil {
		t.Fatalf("Snapshots() error = %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snapshots))
	}
	if snapshots[0].ProviderID != "provider_1" || snapshots[0].DomainCount != 2 {
		t.Fatalf("unexpected snapshot: %+v", snapshots[0])
	}
	if _, err := os.Stat(manager.legacyManifestPath); !os.IsNotExist(err) {
		t.Fatalf("expected runtime.json to be absent, err=%v", err)
	}
}

func TestManagerMigratesLegacyManifestAndPrunesFiles(t *testing.T) {
	tempDir := t.TempDir()
	db := openSubscriptionTestDB(t)
	manager := NewManager(tempDir, tempDir, db, nil, nil)

	if err := os.MkdirAll(manager.runtimeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	domainListPath := filepath.Join(manager.runtimeDir, "legacy.domains.list")
	configPath := filepath.Join(manager.runtimeDir, "legacy.json")
	logPath := filepath.Join(manager.runtimeDir, "legacy.log")
	if err := os.WriteFile(domainListPath, []byte("example.com\nchatgpt.com\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(domain list) error = %v", err)
	}
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	if err := os.WriteFile(logPath, []byte("INFO legacy\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(log) error = %v", err)
	}

	legacy := manifest{
		Instances: []*legacyManagedInstance{
			{
				Key:            "provider_legacy::us",
				ProviderID:     "provider_legacy",
				ProviderName:   "Legacy",
				Location:       "US",
				InterfaceName:  "sblegacy01",
				DomainListPath: domainListPath,
				ConfigPath:     configPath,
				LogPath:        logPath,
				Settings:       config.DefaultRoutingSettings(),
				PID:            4321,
			},
		},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(manager.legacyManifestPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile(runtime.json) error = %v", err)
	}

	snapshots, err := manager.Snapshots()
	if err != nil {
		t.Fatalf("Snapshots() error = %v", err)
	}
	if len(snapshots) != 1 {
		t.Fatalf("expected 1 migrated snapshot, got %d", len(snapshots))
	}
	if snapshots[0].DomainCount != 2 || snapshots[0].ProviderID != "provider_legacy" {
		t.Fatalf("unexpected migrated snapshot: %+v", snapshots[0])
	}

	if _, err := os.Stat(manager.legacyManifestPath); !os.IsNotExist(err) {
		t.Fatalf("expected runtime.json to be removed, err=%v", err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("expected legacy log to be removed, err=%v", err)
	}
	if _, err := os.Stat(domainListPath); !os.IsNotExist(err) {
		t.Fatalf("expected legacy domain list to be removed, err=%v", err)
	}
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("expected active config to remain, err=%v", err)
	}
}

func openSubscriptionTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "vpn-manager.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}
