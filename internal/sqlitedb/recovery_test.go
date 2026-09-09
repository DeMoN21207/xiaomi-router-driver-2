package sqlitedb_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"xiomi-router-driver/internal/domains"
	"xiomi-router-driver/internal/events"
	"xiomi-router-driver/internal/openvpn"
	"xiomi-router-driver/internal/sqlitedb"
	"xiomi-router-driver/internal/subscription"
)

func TestStoresRecoverAfterInitializationWriteFailure(t *testing.T) {
	tests := []struct {
		name    string
		newRead func(*sql.DB, string) func() error
	}{
		{"domains", func(db *sql.DB, dir string) func() error {
			m := domains.NewManager(db, "", "")
			return func() error { _, err := m.List(); return err }
		}},
		{"events", func(db *sql.DB, dir string) func() error {
			s := events.NewStore(db, "")
			return func() error { _, _, err := s.List(10, 0); return err }
		}},
		{"openvpn", func(db *sql.DB, dir string) func() error {
			m := openvpn.NewManager(dir, dir, db, nil, nil)
			return func() error { _, err := m.Snapshots(); return err }
		}},
		{"subscription", func(db *sql.DB, dir string) func() error {
			m := subscription.NewManager(dir, dir, db, nil, nil)
			return func() error { _, err := m.Snapshots(); return err }
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			db, err := sqlitedb.Open(filepath.Join(dir, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			read := tt.newRead(db, dir)
			if _, err := db.Exec("PRAGMA query_only = ON"); err != nil {
				t.Fatal(err)
			}
			if err := read(); err == nil {
				t.Fatal("expected initialization write failure")
			}
			if _, err := db.Exec("PRAGMA query_only = OFF"); err != nil {
				t.Fatal(err)
			}
			if err := read(); err != nil {
				t.Fatalf("store did not recover: %v", err)
			}
		})
	}
}
