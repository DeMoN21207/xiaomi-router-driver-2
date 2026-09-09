package subscription

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyMigrationRetriesWithoutLosingInstances(t *testing.T) {
	db := openSubscriptionTestDB(t)
	dir := t.TempDir()
	m := NewManager(dir, dir, db, nil, nil)
	if err := os.MkdirAll(m.runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureRuntimeSchema(db); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(m.runtimeDir, "first.json")
	second := filepath.Join(m.runtimeDir, "second.json")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	data, err := json.Marshal(manifest{Instances: []*legacyManagedInstance{
		{Key: "first", ProviderID: "first", ConfigPath: first},
		{Key: "second", ProviderID: "second", ConfigPath: second},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.legacyManifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_second BEFORE INSERT ON subscription_runtime_instances WHEN NEW.key = 'second' BEGIN SELECT RAISE(FAIL, 'temporary write failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshots(); err == nil {
		t.Fatal("expected migration failure")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM subscription_runtime_instances`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("partial migration left %d instances; want 0", count)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_second`); err != nil {
		t.Fatal(err)
	}
	got, err := m.Snapshots()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("retry loaded %d instances; want 2", len(got))
	}
	for _, path := range []string{first, second} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("active config lost: %v", err)
		}
	}
}
