package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"xiomi-router-driver/internal/sqlitedb"
)

func TestCheckApplicationStateReportsDatabaseAndInterruptedUpdate(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	dbPath := filepath.Join(dataDir, "vpn-manager.db")
	db, err := sqlitedb.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".update-journal.json"), []byte(`{"phase":"applying"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	checks := checkApplicationState(root)
	levels := map[string]resultLevel{}
	for _, check := range checks {
		levels[check.Name] = check.Level
	}
	if levels["sqlite integrity"] != levelOK {
		t.Fatalf("sqlite check = %v", checks)
	}
	if levels["update transaction"] != levelWarn {
		t.Fatalf("update transaction check = %v", checks)
	}
}
