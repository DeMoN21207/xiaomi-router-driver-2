package status

import (
	"path/filepath"
	"testing"
	"time"

	"xiomi-router-driver/internal/sqlitedb"
)

func TestPurgeTrafficOlderThanRemovesOldObservedDNSRows(t *testing.T) {
	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "vpn-manager.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	service := &Service{
		siteTraffic: newSiteTrafficStore(db),
	}

	if err := service.siteTraffic.ensureReady(); err != nil {
		t.Fatalf("ensureReady() error = %v", err)
	}

	oldAt := time.Date(2026, time.April, 1, 10, 0, 0, 0, time.UTC).Format(time.RFC3339)
	newAt := time.Date(2026, time.April, 20, 10, 0, 0, 0, time.UTC).Format(time.RFC3339)

	for _, stmt := range []string{
		`INSERT INTO site_dns_observations (ip, domain, observed_at) VALUES ('1.1.1.1', 'old.example', '` + oldAt + `')`,
		`INSERT INTO site_dns_observations (ip, domain, observed_at) VALUES ('2.2.2.2', 'new.example', '` + newAt + `')`,
		`INSERT INTO device_traffic_history (source_ip, bucket_at, bytes, packets) VALUES ('192.168.31.10', '` + oldAt + `', 100, 1)`,
		`INSERT INTO device_site_traffic_history (source_ip, domain, bucket_at, bytes, packets) VALUES ('192.168.31.10', 'old.example', '` + oldAt + `', 100, 1)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("Exec(%q) error = %v", stmt, err)
		}
	}

	cutoff := time.Date(2026, time.April, 10, 0, 0, 0, 0, time.UTC)
	if err := service.PurgeTrafficOlderThan(cutoff); err != nil {
		t.Fatalf("PurgeTrafficOlderThan() error = %v", err)
	}

	assertCount := func(query string, want int) {
		t.Helper()
		var got int
		if err := db.QueryRow(query).Scan(&got); err != nil {
			t.Fatalf("QueryRow(%q) error = %v", query, err)
		}
		if got != want {
			t.Fatalf("QueryRow(%q) = %d, want %d", query, got, want)
		}
	}

	assertCount(`SELECT COUNT(1) FROM site_dns_observations`, 1)
	assertCount(`SELECT COUNT(1) FROM device_traffic_history`, 0)
	assertCount(`SELECT COUNT(1) FROM device_site_traffic_history`, 0)
}
