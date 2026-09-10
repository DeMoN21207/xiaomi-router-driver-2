package automation

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/sqlitedb"
	"xiomi-router-driver/internal/status"
)

type recoveryStatusFixture struct {
	snapshot status.Snapshot
}

func (f recoveryStatusFixture) RuntimeSnapshot(context.Context) (status.Snapshot, error) {
	return f.snapshot, nil
}

func (f recoveryStatusFixture) PurgeTrafficOlderThan(time.Time) error { return nil }

func TestTickRecoversMissingVPNRuntimeWhileWANProbeIsDown(t *testing.T) {
	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "vpn-manager.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	manager := config.NewManager(db, "")
	state := config.DefaultState()
	state.Automation.AutoRecover = true
	state.Automation.ProviderFailover = false
	state.Providers = []config.Provider{{ID: "vpn", Name: "VPN", Type: config.ProviderTypeOpenVPN, Source: "provider.ovpn", Enabled: true}}
	state.Rules = []config.Rule{{ID: "rule", Name: "Rule", ProviderID: "vpn", Domains: []string{"example.com"}, Enabled: true}}
	if _, err := manager.Save(state); err != nil {
		t.Fatal(err)
	}
	loaded, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Automation.AutoRecover || !openvpnRecoveryNeeded(loaded, nil) {
		t.Fatalf("saved recovery fixture is invalid: %+v", loaded)
	}

	applied := 0
	supervisor := NewSupervisor(manager, recoveryStatusFixture{
		snapshot: status.Snapshot{WAN: status.WANStatus{State: "down"}},
	}, func(context.Context) error { applied++; return nil }, func(context.Context, config.State) error {
		applied++
		return nil
	}, nil, t.TempDir())
	supervisor.lastWAN = "down"

	supervisor.tick(context.Background())

	if applied != 1 {
		t.Fatalf("runtime recovery apply count = %d, want 1", applied)
	}
}
