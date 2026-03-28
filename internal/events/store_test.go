package events

import (
	"database/sql"
	"path/filepath"
	"testing"

	"xiomi-router-driver/internal/sqlitedb"
)

func TestStoreAddListClearWithSQLite(t *testing.T) {
	db := openEventsTestDB(t)
	store := NewStore(db, filepath.Join(t.TempDir(), "events.json"))

	for index := 0; index < maxEvents+5; index++ {
		if _, err := store.Add("info", "kind.test", "message"); err != nil {
			t.Fatalf("Add() error = %v", err)
		}
	}

	list, _, err := store.List(0, 0)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(list) != maxEvents {
		t.Fatalf("expected %d events, got %d", maxEvents, len(list))
	}

	if err := store.Clear(); err != nil {
		t.Fatalf("Clear() error = %v", err)
	}

	list, _, err = store.List(0, 0)
	if err != nil {
		t.Fatalf("List() after clear error = %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected cleared events, got %d", len(list))
	}
}

func openEventsTestDB(t *testing.T) *sql.DB {
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
