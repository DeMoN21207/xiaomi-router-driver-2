package api

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/domains"
	"xiomi-router-driver/internal/openvpn"
)

func TestApplyPreservesSettingsSavedAfterSnapshot(t *testing.T) {
	for _, failApply := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failApply], func(t *testing.T) {
			dir := t.TempDir()
			db := openAPITestDB(t, filepath.Join(dir, "state.db"))
			manager := config.NewManager(db, "")
			initial := config.DefaultState()
			initial.LastAppliedAt = "2026-01-01T00:00:00Z"
			if failApply {
				initial.Providers = []config.Provider{{ID: "vpn", Name: "VPN", Type: config.ProviderTypeOpenVPN, Source: "missing.ovpn", Enabled: true}}
				initial.Rules = []config.Rule{{ID: "rule", Name: "Rule", ProviderID: "vpn", Domains: []string{"example.com"}, Enabled: true}}
			}
			stale, err := manager.Save(initial)
			if err != nil {
				t.Fatal(err)
			}
			newer, err := manager.Load()
			if err != nil {
				t.Fatal(err)
			}
			newer.Automation.TrafficCleanupDays = 7
			newer.Update.Repository = "example/new-repository"
			newer.Providers = append(newer.Providers, config.Provider{ID: "new-vpn", Name: "New VPN", Type: config.ProviderTypeOpenVPN, Source: "new.ovpn"})
			newer.Rules = append(newer.Rules, config.Rule{ID: "new-rule", Name: "Saved while applying", ProviderID: "new-vpn", Domains: []string{"new.example.com"}})
			if _, err := manager.Save(newer); err != nil {
				t.Fatal(err)
			}
			h := NewHandler(Dependencies{
				State:   manager,
				Domains: domains.NewManager(db, "", ""),
				OpenVPN: openvpn.NewManager(dir, dir, db, nil, nil),
				DataDir: dir,
			})
			_, err = h.applyStateRules(context.Background(), stale, true)
			if (err != nil) != failApply {
				t.Fatalf("apply error = %v, want failure %v", err, failApply)
			}
			got, err := manager.Load()
			if err != nil {
				t.Fatal(err)
			}
			if got.Automation.TrafficCleanupDays != 7 || got.Update.Repository != "example/new-repository" || len(got.Rules) != len(newer.Rules) {
				t.Fatalf("applying an earlier snapshot overwrote newer settings: %+v", got)
			}
			if failApply && (got.LastError == "" || got.LastAppliedAt != initial.LastAppliedAt) {
				t.Fatalf("failure metadata = %q / %q", got.LastError, got.LastAppliedAt)
			}
			if !failApply && (got.LastError != "" || got.LastAppliedAt == initial.LastAppliedAt) {
				t.Fatalf("success metadata = %q / %q", got.LastError, got.LastAppliedAt)
			}
		})
	}
}

func TestApplyReportsMetadataWriteFailure(t *testing.T) {
	dir := t.TempDir()
	db := openAPITestDB(t, filepath.Join(dir, "state.db"))
	manager := config.NewManager(db, "")
	state, err := manager.Save(config.DefaultState())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_apply_metadata BEFORE INSERT ON app_meta WHEN NEW.key = 'lastAppliedAt' BEGIN SELECT RAISE(FAIL, 'disk write failed'); END`); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(Dependencies{
		State:   manager,
		Domains: domains.NewManager(db, "", ""),
		OpenVPN: openvpn.NewManager(dir, dir, db, nil, nil),
		DataDir: dir,
	})
	if _, err := h.applyStateRules(context.Background(), state, true); err == nil || !strings.Contains(err.Error(), "disk write failed") {
		t.Fatalf("expected metadata write failure, got %v", err)
	}
}
