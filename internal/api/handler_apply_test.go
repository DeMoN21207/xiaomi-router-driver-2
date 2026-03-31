package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/domains"
	"xiomi-router-driver/internal/openvpn"
	"xiomi-router-driver/internal/sqlitedb"
)

func TestHandleRuleApplyRollbackOnFailure(t *testing.T) {
	tempDir := t.TempDir()
	db := openAPITestDB(t, filepath.Join(tempDir, "vpn-manager.db"))
	stateManager := config.NewManager(db, filepath.Join(tempDir, "vpn-state.json"))
	domainsManager := domains.NewManager(db, filepath.Join(tempDir, "domains.current"), filepath.Join(tempDir, "domains.legacy"))
	openvpnManager := openvpn.NewManager(tempDir, tempDir, db, nil, nil)
	handler := NewHandler(Dependencies{
		State:     stateManager,
		Domains:   domainsManager,
		OpenVPN:   openvpnManager,
		DataDir:   tempDir,
		Events:    nil,
		Routing:   nil,
		Status:    nil,
		Blacklist: nil,
	})

	initialState := config.State{
		Providers: []config.Provider{
			{
				ID:      "provider-openvpn",
				Name:    "FizzVPN",
				Type:    config.ProviderTypeOpenVPN,
				Source:  "profiles/missing.ovpn",
				Enabled: true,
			},
		},
		Rules: []config.Rule{
			{
				ID:               "rule-1",
				Name:             "Media",
				ProviderID:       "provider-openvpn",
				SelectedLocation: "NL",
				Domains:          []string{"youtube.com"},
				Enabled:          false,
			},
		},
		Routing:    config.DefaultRoutingSettings(),
		Automation: config.DefaultAutomationSettings(),
	}
	if _, err := stateManager.Save(initialState); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	requestBody, err := json.Marshal(map[string]any{
		"name":             "Media",
		"providerId":       "provider-openvpn",
		"selectedLocation": "NL",
		"domains":          "youtube.com,googlevideo.com",
		"enabled":          true,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/rules/rule-1?apply=1", bytes.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("ServeHTTP() status = %d, want %d, body=%s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}

	loaded, err := stateManager.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(loaded.Rules) != 1 {
		t.Fatalf("expected one rule after rollback, got %+v", loaded.Rules)
	}
	rule := loaded.Rules[0]
	if rule.Enabled {
		t.Fatalf("expected rule to be rolled back to disabled, got %+v", rule)
	}
	if len(rule.Domains) != 1 || rule.Domains[0] != "youtube.com" {
		t.Fatalf("expected original domains after rollback, got %+v", rule.Domains)
	}
	if loaded.LastError != "" {
		t.Fatalf("expected rollback to restore clean lastError, got %q", loaded.LastError)
	}

	currentDomains, err := domainsManager.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(currentDomains) != 0 {
		t.Fatalf("expected rollback to restore applied domains to empty, got %+v", currentDomains)
	}
}

func openAPITestDB(t *testing.T, path string) *sql.DB {
	t.Helper()

	db, err := sqlitedb.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}
