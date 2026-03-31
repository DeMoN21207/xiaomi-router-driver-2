package config

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"xiomi-router-driver/internal/sqlitedb"
)

func TestManagerRoundTripSQLite(t *testing.T) {
	db := openTestDB(t)
	manager := NewManager(db, filepath.Join(t.TempDir(), "vpn-state.json"))

	state := State{
		Providers: []Provider{
			{ID: "provider_1", Name: "Fizz", Type: ProviderTypeSubscription, Source: "https://example.com/sub", Enabled: true},
		},
		Rules: []Rule{
			{ID: "rule_1", Name: "OpenAI", ProviderID: "provider_1", SelectedLocation: "USA", Domains: []string{"OpenAI.com", "chatgpt.com"}, Enabled: true},
		},
		Routing: RoutingSettings{
			VPNGateway:        "10.8.0.1",
			VPNRouteMode:      "gateway",
			VPNMasquerade:     true,
			LANIface:          "br-lan",
			VPNIface:          "tun0",
			TableNum:          101,
			FWZoneChain:       "zone_lan_forward",
			IPSetName:         "vpn_hosts",
			FWMark:            "0x1",
			DNSMasqConfigFile: "/tmp/dnsmasq.d/vpn_dns.conf",
		},
		Automation:    AutomationSettings{InstallService: true, AutoRecover: true},
		LastAppliedAt: "2026-03-25T00:00:00Z",
		LastError:     "",
	}

	if _, err := manager.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded, err := manager.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(loaded.Providers) != 1 || loaded.Providers[0].ID != "provider_1" {
		t.Fatalf("unexpected providers: %+v", loaded.Providers)
	}
	if len(loaded.Rules) != 1 || len(loaded.Rules[0].Domains) != 2 {
		t.Fatalf("unexpected rules: %+v", loaded.Rules)
	}
	if loaded.Rules[0].Domains[0] != "openai.com" {
		t.Fatalf("expected normalized domains, got %+v", loaded.Rules[0].Domains)
	}
	if !loaded.Automation.InstallService || !loaded.Automation.AutoRecover {
		t.Fatalf("unexpected automation settings: %+v", loaded.Automation)
	}
	if loaded.UpdatedAt == "" {
		t.Fatalf("expected updatedAt to be set")
	}
}

func TestManagerMigratesLegacyJSON(t *testing.T) {
	tempDir := t.TempDir()
	db := openTestDB(t)
	legacyPath := filepath.Join(tempDir, "vpn-state.json")

	legacy := State{
		Providers: []Provider{
			{ID: "provider_legacy", Name: "Legacy", Type: ProviderTypeOpenVPN, Source: "profiles/demo.ovpn", Enabled: true},
		},
		Rules: []Rule{
			{ID: "rule_legacy", Name: "Legacy Rule", ProviderID: "provider_legacy", Domains: []string{"example.com"}, Enabled: true},
		},
		Routing:       DefaultRoutingSettings(),
		Automation:    DefaultAutomationSettings(),
		LastAppliedAt: "2026-03-24T00:00:00Z",
		LastError:     "legacy",
		UpdatedAt:     "2026-03-24T00:00:00Z",
	}

	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(legacyPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	manager := NewManager(db, legacyPath)
	loaded, err := manager.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(loaded.Providers) != 1 || loaded.Providers[0].ID != "provider_legacy" {
		t.Fatalf("unexpected migrated providers: %+v", loaded.Providers)
	}
	if loaded.LastError != "legacy" {
		t.Fatalf("expected migrated meta, got %+v", loaded)
	}
}

func TestManagerUpdateRuleSQLite(t *testing.T) {
	db := openTestDB(t)
	manager := NewManager(db, filepath.Join(t.TempDir(), "vpn-state.json"))

	state := State{
		Providers: []Provider{
			{ID: "provider_1", Name: "Fizz", Type: ProviderTypeSubscription, Source: "https://example.com/sub", Enabled: true},
		},
		Rules: []Rule{
			{ID: "rule_1", Name: "OpenAI", ProviderID: "provider_1", SelectedLocation: "USA", Domains: []string{"openai.com"}, Enabled: true},
			{ID: "rule_2", Name: "Media", ProviderID: "provider_1", SelectedLocation: "NL", Domains: []string{"youtube.com"}, Enabled: true},
		},
		Routing:    DefaultRoutingSettings(),
		Automation: DefaultAutomationSettings(),
	}

	saved, err := manager.Save(state)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	time.Sleep(1100 * time.Millisecond)

	updated, err := manager.UpdateRule(Rule{
		ID:               "rule_2",
		Name:             "Media Updated",
		ProviderID:       "provider_1",
		SelectedLocation: "NL",
		Domains:          []string{"YouTube.com", "googlevideo.com"},
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("UpdateRule() error = %v", err)
	}
	if updated.Name != "Media Updated" {
		t.Fatalf("unexpected updated rule: %+v", updated)
	}

	loaded, err := manager.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(loaded.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %+v", loaded.Rules)
	}
	if loaded.Rules[1].Name != "Media Updated" {
		t.Fatalf("expected second rule to be updated, got %+v", loaded.Rules[1])
	}
	if len(loaded.Rules[1].Domains) != 2 || loaded.Rules[1].Domains[0] != "youtube.com" || loaded.Rules[1].Domains[1] != "googlevideo.com" {
		t.Fatalf("expected normalized updated domains, got %+v", loaded.Rules[1].Domains)
	}
	if loaded.Rules[0].Name != state.Rules[0].Name {
		t.Fatalf("unexpected first rule mutation: %+v", loaded.Rules[0])
	}
	if loaded.UpdatedAt == "" || loaded.UpdatedAt == saved.UpdatedAt {
		t.Fatalf("expected updatedAt to change, before=%q after=%q", saved.UpdatedAt, loaded.UpdatedAt)
	}
}

func TestManagerDeleteRuleSQLite(t *testing.T) {
	db := openTestDB(t)
	manager := NewManager(db, filepath.Join(t.TempDir(), "vpn-state.json"))

	state := State{
		Providers: []Provider{
			{ID: "provider_1", Name: "Fizz", Type: ProviderTypeSubscription, Source: "https://example.com/sub", Enabled: true},
		},
		Rules: []Rule{
			{ID: "rule_1", Name: "First", ProviderID: "provider_1", SelectedLocation: "USA", Domains: []string{"openai.com"}, Enabled: true},
			{ID: "rule_2", Name: "Second", ProviderID: "provider_1", SelectedLocation: "NL", Domains: []string{"youtube.com"}, Enabled: true},
		},
		Routing:    DefaultRoutingSettings(),
		Automation: DefaultAutomationSettings(),
	}

	if _, err := manager.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if err := manager.DeleteRule("rule_1"); err != nil {
		t.Fatalf("DeleteRule() error = %v", err)
	}

	loaded, err := manager.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if len(loaded.Rules) != 1 {
		t.Fatalf("expected 1 rule after delete, got %+v", loaded.Rules)
	}
	if loaded.Rules[0].ID != "rule_2" || loaded.Rules[0].Name != "Second" {
		t.Fatalf("expected remaining rule to keep order, got %+v", loaded.Rules[0])
	}
}

func openTestDB(t *testing.T) *sql.DB {
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
