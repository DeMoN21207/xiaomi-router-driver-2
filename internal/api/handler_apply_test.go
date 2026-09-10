package api

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/domains"
	"xiomi-router-driver/internal/openvpn"
	"xiomi-router-driver/internal/sqlitedb"
)

func TestHandleManualApplyRunsAsTrackableOperation(t *testing.T) {
	tempDir := t.TempDir()
	db := openAPITestDB(t, filepath.Join(tempDir, "vpn-manager.db"))
	stateManager := config.NewManager(db, "")
	if _, err := stateManager.Save(config.DefaultState()); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Dependencies{
		State:   stateManager,
		Domains: domains.NewManager(db, "", ""),
		OpenVPN: openvpn.NewManager(tempDir, tempDir, db, nil, nil),
		DataDir: tempDir,
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/rules/apply", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("apply status = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var started struct {
		Operation applyOperation `json:"operation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.Operation.ID == "" {
		t.Fatal("apply response did not include an operation ID")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		statusRec := httptest.NewRecorder()
		handler.ServeHTTP(statusRec, httptest.NewRequest(http.MethodGet, "/api/rules/apply/"+started.Operation.ID, nil))
		if statusRec.Code != http.StatusOK {
			t.Fatalf("operation status = %d: %s", statusRec.Code, statusRec.Body.String())
		}
		var payload struct {
			Operation applyOperation `json:"operation"`
		}
		if err := json.Unmarshal(statusRec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Operation.Status == "succeeded" {
			return
		}
		if payload.Operation.Status == "failed" {
			t.Fatalf("apply operation failed: %+v", payload.Operation)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("apply operation did not finish")
}

func TestHandleRuleApplyRollbackOnFailure(t *testing.T) {
	tempDir := t.TempDir()
	db := openAPITestDB(t, filepath.Join(tempDir, "vpn-manager.db"))
	stateManager := config.NewManager(db, filepath.Join(tempDir, "vpn-state.json"))
	domainsManager := domains.NewManager(db, filepath.Join(tempDir, "domains.current"), filepath.Join(tempDir, "domains.legacy"))
	openvpnManager := openvpn.NewManager(tempDir, tempDir, db, nil, nil)
	handler := NewHandler(Dependencies{
		State:   stateManager,
		Domains: domainsManager,
		OpenVPN: openvpnManager,
		DataDir: tempDir,
		Events:  nil,
		Routing: nil,
		Status:  nil,
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

func TestHandleManualApplyRestoresCurrentDomainsOnFailure(t *testing.T) {
	tempDir := t.TempDir()
	db := openAPITestDB(t, filepath.Join(tempDir, "vpn-manager.db"))
	stateManager := config.NewManager(db, filepath.Join(tempDir, "vpn-state.json"))
	domainsManager := domains.NewManager(db, filepath.Join(tempDir, "domains.current"), filepath.Join(tempDir, "domains.legacy"))
	openvpnManager := openvpn.NewManager(tempDir, tempDir, db, nil, nil)
	handler := NewHandler(Dependencies{
		State:   stateManager,
		Domains: domainsManager,
		OpenVPN: openvpnManager,
		DataDir: tempDir,
	})

	if err := domainsManager.ReplaceAll([]string{"previous.example.com"}); err != nil {
		t.Fatalf("ReplaceAll() error = %v", err)
	}

	state := config.State{
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
				Domains:          []string{"new.example.com"},
				Enabled:          true,
			},
		},
		Routing:    config.DefaultRoutingSettings(),
		Automation: config.DefaultAutomationSettings(),
	}
	if _, err := stateManager.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/rules/apply", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	operation := waitForApplyOperation(t, handler, rec)
	if operation.Status != "failed" {
		t.Fatalf("apply operation = %+v, want failed", operation)
	}

	currentDomains, err := domainsManager.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(currentDomains) != 1 || currentDomains[0] != "previous.example.com" {
		t.Fatalf("expected failed apply to restore previous domains, got %+v", currentDomains)
	}
}

func waitForApplyOperation(t *testing.T, handler *Handler, started *httptest.ResponseRecorder) applyOperation {
	t.Helper()
	if started.Code != http.StatusAccepted {
		t.Fatalf("apply start status = %d, want 202: %s", started.Code, started.Body.String())
	}
	var payload struct {
		Operation applyOperation `json:"operation"`
	}
	if err := json.Unmarshal(started.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/rules/apply/"+payload.Operation.ID, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("apply status request = %d: %s", recorder.Code, recorder.Body.String())
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Operation.Status == "succeeded" || payload.Operation.Status == "failed" {
			return payload.Operation
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("apply operation did not finish")
	return applyOperation{}
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
