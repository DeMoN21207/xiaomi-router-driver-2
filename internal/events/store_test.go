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

func TestStoreListByLevelPaginatesAndCountsLevels(t *testing.T) {
	db := openEventsTestDB(t)
	store := NewStore(db, filepath.Join(t.TempDir(), "events.json"))

	for _, level := range []string{"info", "error", "warn", "error"} {
		if _, err := store.Add(level, "kind.test", "message"); err != nil {
			t.Fatalf("Add(%s) error = %v", level, err)
		}
	}

	list, total, err := store.ListByLevel("error", 1, 1)
	if err != nil {
		t.Fatalf("ListByLevel() error = %v", err)
	}
	if total != 2 {
		t.Fatalf("expected filtered total 2, got %d", total)
	}
	if len(list) != 1 {
		t.Fatalf("expected one paged event, got %d", len(list))
	}
	if list[0].Level != "error" {
		t.Fatalf("expected only error events, got %+v", list)
	}

	counts, err := store.CountByLevel()
	if err != nil {
		t.Fatalf("CountByLevel() error = %v", err)
	}
	if counts["info"] != 1 || counts["warn"] != 1 || counts["error"] != 2 {
		t.Fatalf("unexpected level counts: %+v", counts)
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
