package sqlitedb

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRestoresCorruptDatabaseFromLastMaintenanceBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpn-manager.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE sample (value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO sample (value) VALUES ('preserved')`); err != nil {
		t.Fatal(err)
	}
	if err := Maintain(db, path); err != nil {
		t.Fatalf("Maintain() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}

	restored, err := Open(path)
	if err != nil {
		t.Fatalf("Open() did not recover database: %v", err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	var value string
	if err := restored.QueryRow(`SELECT value FROM sample`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "preserved" {
		t.Fatalf("restored value = %q, want preserved", value)
	}
}

func TestOpenDoesNotReplaceCorruptDatabaseWithoutBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vpn-manager.db")
	want := []byte("not a sqlite database")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}

	if db, err := Open(path); err == nil {
		_ = db.Close()
		t.Fatal("Open() succeeded for a corrupt database without a backup")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("corrupt database was replaced: %q", got)
	}
}
