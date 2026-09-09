package status

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"xiomi-router-driver/internal/sqlitedb"
)

func TestTrafficStoresRecoverAfterInitializationWriteFailure(t *testing.T) {
	tests := []struct {
		name      string
		newEnsure func(*sql.DB) func() error
	}{
		{"history", func(db *sql.DB) func() error {
			s := newTrafficHistoryStore(db, "", 24*time.Hour)
			return func() error { s.mu.Lock(); defer s.mu.Unlock(); return s.ensureReadyLocked() }
		}},
		{"domain traffic", func(db *sql.DB) func() error { return newDomainTrafficStore(db).ensureReady }},
		{"domain health", func(db *sql.DB) func() error { return newDomainHealthStore(db).ensureReady }},
		{"site traffic", func(db *sql.DB) func() error { return newSiteTrafficStore(db).ensureReady }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ensure := tt.newEnsure(db)
			if _, err := db.Exec("PRAGMA query_only = ON"); err != nil {
				t.Fatal(err)
			}
			if err := ensure(); err == nil {
				t.Fatal("expected initialization write failure")
			}
			if _, err := db.Exec("PRAGMA query_only = OFF"); err != nil {
				t.Fatal(err)
			}
			if err := ensure(); err != nil {
				t.Fatalf("store did not recover: %v", err)
			}
		})
	}
}
