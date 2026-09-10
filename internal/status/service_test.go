package status

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/dnsproxy"
	"xiomi-router-driver/internal/domains"
	"xiomi-router-driver/internal/sqlitedb"
)

func TestParseDefaultRouteInterface(t *testing.T) {
	output := "default via 192.168.31.1 dev eth0.2 proto static src 192.168.31.2\n"
	if got := parseDefaultRouteInterface(output); got != "eth0.2" {
		t.Fatalf("parseDefaultRouteInterface() = %q, want eth0.2", got)
	}
}

func TestProbeWANUsesInterfaceAndSucceedsWhenAnyTargetReplies(t *testing.T) {
	var calls [][]string
	service := &Service{
		wanProbes:       []string{"1.1.1.1", "8.8.8.8"},
		wanProbeTimeout: time.Second,
		wanInterface: func(context.Context) string {
			return "eth0.2"
		},
		wanCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, append([]string{name}, args...))
			if args[len(args)-1] == "1.1.1.1" {
				return []byte("unreachable"), errors.New("exit 1")
			}
			return []byte("64 bytes time=12.3 ms"), nil
		},
	}

	got := service.probeWAN(context.Background())
	if got.State != "up" || got.Probe != "8.8.8.8" || got.Interface != "eth0.2" {
		t.Fatalf("probeWAN() = %+v", got)
	}
	wantSecond := []string{"ping", "-c", "1", "-W", "1", "-I", "eth0.2", "8.8.8.8"}
	if len(calls) != 2 || !reflect.DeepEqual(calls[1], wantSecond) {
		t.Fatalf("probe calls = %v, want second %v", calls, wantSecond)
	}
	if strings.Contains(got.LastError, "1.1.1.1") {
		t.Fatalf("successful fallback retained failure: %+v", got)
	}
}

func TestServiceSnapshotIncludesBundleInfo(t *testing.T) {
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "bundle-info.txt"), []byte("binary=vpn-manager\ngoos=linux\ngoarch=arm64\ndefault_port=18080\nversion=v1.2.3\ncommit=abc1234\nbuilt_at=2026-07-08T10:30:00Z\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	uptimePath := filepath.Join(tempDir, "uptime")
	if err := os.WriteFile(uptimePath, []byte("90061.23 120.00\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(uptime) error = %v", err)
	}

	db, err := sqlitedb.Open(filepath.Join(dataDir, "vpn-manager.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	stateManager := config.NewManager(db, filepath.Join(dataDir, "vpn-state.json"))
	if _, err := stateManager.Save(config.DefaultState()); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	domainsManager := domains.NewManager(db, filepath.Join(dataDir, "domains.list"), filepath.Join(dataDir, "domains.legacy"))
	service := NewService(stateManager, domainsManager, nil, nil, filepath.Join(dataDir, ".vpn-manager", "update_routes.sh"), tempDir, dataDir, db, filepath.Join(dataDir, "traffic-history.json"))
	service.uptimePath = uptimePath
	service.SetDNSProxyHealth(func() dnsproxy.Health { return dnsproxy.Health{State: "fallback", Failures: 3} })

	snapshot, err := service.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.UptimeSeconds != 90061 {
		t.Fatalf("Snapshot().UptimeSeconds = %d, want 90061", snapshot.UptimeSeconds)
	}
	if snapshot.UptimeFormatted != "1д 1ч 1м" {
		t.Fatalf("Snapshot().UptimeFormatted = %q, want formatted uptime", snapshot.UptimeFormatted)
	}
	if snapshot.Bundle == nil {
		t.Fatal("Snapshot().Bundle = nil, want bundle info")
	}
	if snapshot.Bundle.Version != "v1.2.3" {
		t.Fatalf("Bundle.Version = %q, want v1.2.3", snapshot.Bundle.Version)
	}
	if snapshot.Bundle.Commit != "abc1234" {
		t.Fatalf("Bundle.Commit = %q, want abc1234", snapshot.Bundle.Commit)
	}
	if snapshot.Bundle.BuiltAt != "2026-07-08T10:30:00Z" {
		t.Fatalf("Bundle.BuiltAt = %q, want build timestamp", snapshot.Bundle.BuiltAt)
	}
	if snapshot.Bundle.DefaultPort != "18080" {
		t.Fatalf("Bundle.DefaultPort = %q, want 18080", snapshot.Bundle.DefaultPort)
	}
	if snapshot.DNSProxy.State != "fallback" || snapshot.DNSProxy.Failures != 3 {
		t.Fatalf("Snapshot().DNSProxy = %+v", snapshot.DNSProxy)
	}
}
