package api

import (
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/update"
)

func TestValidateRuleEntriesRejectsDuplicateAcrossActiveProviders(t *testing.T) {
	providers := []config.Provider{
		testProvider("provider-a", "Alpha", true),
		testProvider("provider-b", "Beta", true),
	}
	existingRules := []config.Rule{
		testRule("rule-a", "Rule A", "provider-a", "Warsaw", []string{"chatgpt.com"}, true),
	}
	candidate := testRule("rule-b", "Rule B", "provider-b", "Prague", []string{"chatgpt.com"}, true)

	err := validateRuleEntries(candidate, providers, existingRules)
	if err == nil {
		t.Fatal("validateRuleEntries() error = nil, want duplicate error")
	}
	for _, fragment := range []string{`entry "chatgpt.com"`, `Alpha / Warsaw`, `Beta / Prague`} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("validateRuleEntries() error = %q, want fragment %q", err.Error(), fragment)
		}
	}
}

func TestUpdateStatusEndpoint(t *testing.T) {
	tempDir := t.TempDir()
	db := openAPITestDB(t, tempDir+"/vpn-manager.db")
	stateManager := config.NewManager(db, tempDir+"/vpn-state.json")
	updateManager := update.NewManager(update.Options{
		AppDir:  tempDir,
		DataDir: tempDir + "/data",
		State:   stateManager,
		Restart: func() {},
	})
	handler := NewHandler(Dependencies{
		State:   stateManager,
		Update:  updateManager,
		DataDir: tempDir + "/data",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/system/update", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ServeHTTP() status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"repository":"DeMoN21207/xiaomi-router-driver-2"`) {
		t.Fatalf("expected default update repository in response, got %s", rec.Body.String())
	}
}

func TestUpdateUploadRejectsNonLinux(t *testing.T) {
	tempDir := t.TempDir()
	db := openAPITestDB(t, tempDir+"/vpn-manager.db")
	stateManager := config.NewManager(db, tempDir+"/vpn-state.json")
	updateManager := update.NewManager(update.Options{
		AppDir:    tempDir,
		DataDir:   tempDir + "/data",
		State:     stateManager,
		RuntimeOS: "darwin",
		Restart:   func() {},
	})
	handler := NewHandler(Dependencies{
		State:   stateManager,
		Update:  updateManager,
		DataDir: tempDir + "/data",
	})

	body := &strings.Builder{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("archive", "bundle.tar.gz")
	if err != nil {
		t.Fatalf("CreateFormFile() error = %v", err)
	}
	if _, err := part.Write([]byte("not an archive")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/system/update/upload", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ServeHTTP() status = %d, want %d, body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Linux") {
		t.Fatalf("expected Linux unsupported message, got %s", rec.Body.String())
	}
}

func TestProviderRefreshEndpointRefreshesSubscriptionWithoutActiveRules(t *testing.T) {
	tempDir := t.TempDir()
	db := openAPITestDB(t, filepath.Join(tempDir, "vpn-manager.db"))
	stateManager := config.NewManager(db, filepath.Join(tempDir, "vpn-state.json"))
	state := config.DefaultState()
	state.Providers = []config.Provider{
		{
			ID:      "provider-sub",
			Name:    "Sub",
			Type:    config.ProviderTypeSubscription,
			Source:  testSubscriptionSource(),
			Enabled: true,
		},
	}
	if _, err := stateManager.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	handler := NewHandler(Dependencies{
		State:   stateManager,
		DataDir: tempDir,
	})

	req := httptest.NewRequest(http.MethodPost, "/api/providers/provider-sub/refresh", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ServeHTTP() status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Status  string `json:"status"`
		Entries int    `json:"entries"`
		Applied bool   `json:"applied"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Status != "refreshed" {
		t.Fatalf("status = %q, want refreshed", payload.Status)
	}
	if payload.Entries == 0 {
		t.Fatalf("expected refreshed entries count, got %d", payload.Entries)
	}
	if payload.Applied {
		t.Fatalf("expected no apply without active rules")
	}
}

func TestValidateRuleEntriesIgnoresDisabledProviders(t *testing.T) {
	providers := []config.Provider{
		testProvider("provider-a", "Alpha", false),
		testProvider("provider-b", "Beta", true),
	}
	existingRules := []config.Rule{
		testRule("rule-a", "Rule A", "provider-a", "Warsaw", []string{"chatgpt.com"}, true),
	}
	candidate := testRule("rule-b", "Rule B", "provider-b", "Prague", []string{"chatgpt.com"}, true)

	if err := validateRuleEntries(candidate, providers, existingRules); err != nil {
		t.Fatalf("validateRuleEntries() error = %v, want nil", err)
	}
}

func TestValidateRuleEntriesRejectsOverlappingCIDR(t *testing.T) {
	providers := []config.Provider{
		testProvider("provider-a", "Alpha", true),
		testProvider("provider-b", "Beta", true),
	}
	existingRules := []config.Rule{
		testRule("rule-a", "Rule A", "provider-a", "Warsaw", []string{"149.154.160.0/20"}, true),
	}
	candidate := testRule("rule-b", "Rule B", "provider-b", "Prague", []string{"149.154.167.41"}, true)

	err := validateRuleEntries(candidate, providers, existingRules)
	if err == nil {
		t.Fatal("validateRuleEntries() error = nil, want overlap error")
	}
	for _, fragment := range []string{`entry "149.154.167.41" overlaps with "149.154.160.0/20"`, `Alpha / Warsaw`, `Beta / Prague`} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("validateRuleEntries() error = %q, want fragment %q", err.Error(), fragment)
		}
	}
}

func TestValidateProviderActivationRejectsDuplicates(t *testing.T) {
	providers := []config.Provider{
		testProvider("provider-a", "Alpha", false),
		testProvider("provider-b", "Beta", true),
	}
	rules := []config.Rule{
		testRule("rule-a", "Rule A", "provider-a", "Warsaw", []string{"youtube.com"}, true),
		testRule("rule-b", "Rule B", "provider-b", "Prague", []string{"youtube.com"}, true),
	}

	err := validateProviderActivation(testProvider("provider-a", "Alpha", true), providers, rules)
	if err == nil {
		t.Fatal("validateProviderActivation() error = nil, want duplicate error")
	}
	for _, fragment := range []string{`entry "youtube.com"`, `Alpha / Warsaw`, `Beta / Prague`} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("validateProviderActivation() error = %q, want fragment %q", err.Error(), fragment)
		}
	}
}

func TestValidateActiveRuleEntriesRejectsDuplicates(t *testing.T) {
	state := config.State{
		Providers: []config.Provider{
			testProvider("provider-a", "Alpha", true),
			testProvider("provider-b", "Beta", true),
		},
		Rules: []config.Rule{
			testRule("rule-a", "Rule A", "provider-a", "Warsaw", []string{"oaistatic.com"}, true),
			testRule("rule-b", "Rule B", "provider-b", "Prague", []string{"oaistatic.com"}, true),
		},
	}

	err := validateActiveRuleEntries(state)
	if err == nil {
		t.Fatal("validateActiveRuleEntries() error = nil, want duplicate error")
	}
	if !strings.Contains(err.Error(), `entry "oaistatic.com"`) {
		t.Fatalf("validateActiveRuleEntries() error = %q, want duplicated entry in message", err.Error())
	}
}

func TestValidateActiveRuleEntriesIgnoresDisabledRules(t *testing.T) {
	state := config.State{
		Providers: []config.Provider{
			testProvider("provider-a", "Alpha", true),
			testProvider("provider-b", "Beta", true),
		},
		Rules: []config.Rule{
			testRule("rule-a", "Rule A", "provider-a", "Warsaw", []string{"oaistatic.com"}, true),
			testRule("rule-b", "Rule B", "provider-b", "Prague", []string{"oaistatic.com"}, false),
		},
	}

	if err := validateActiveRuleEntries(state); err != nil {
		t.Fatalf("validateActiveRuleEntries() error = %v, want nil", err)
	}
}

func TestBuildPriorityPolicyRejectsScheduleOverlap(t *testing.T) {
	handler := &Handler{dataDir: t.TempDir()}
	state := config.State{
		Providers: []config.Provider{testProvider("provider-a", "Alpha", true)},
	}
	state.Providers[0].Source = testSubscriptionSource()

	_, err := handler.buildPriorityPolicy("", priorityPolicyRequest{
		Name:       "Main",
		ProviderID: "provider-a",
		Enabled:    true,
		Targets:    []config.PriorityTarget{{Location: "Germany"}, {Location: "Netherlands"}},
		Schedule: []config.PriorityScheduleWindow{
			{Start: "09:00", End: "12:00", Location: "Germany"},
			{Start: "11:00", End: "13:00", Location: "Netherlands"},
		},
	}, state)
	if err == nil {
		t.Fatal("buildPriorityPolicy() error = nil, want overlap error")
	}
	if !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("buildPriorityPolicy() error = %q, want overlap", err.Error())
	}
}

func TestBuildPriorityPolicyAllowsEmptyEntries(t *testing.T) {
	handler := &Handler{dataDir: t.TempDir()}
	state := config.State{
		Providers: []config.Provider{testProvider("provider-a", "Alpha", true)},
	}
	state.Providers[0].Source = testSubscriptionSource()

	policy, err := handler.buildPriorityPolicy("", priorityPolicyRequest{
		Name:       "Main",
		ProviderID: "provider-a",
		Enabled:    true,
		Targets:    []config.PriorityTarget{{Location: "Germany"}, {Location: "Netherlands"}},
	}, state)
	if err != nil {
		t.Fatalf("buildPriorityPolicy() error = %v, want nil", err)
	}
	if len(policy.Entries) != 0 {
		t.Fatalf("buildPriorityPolicy() entries = %+v, want empty", policy.Entries)
	}
}

func TestValidateActiveRuleEntriesIgnoresPriorityPolicyEntries(t *testing.T) {
	state := config.State{
		Providers: []config.Provider{
			testProvider("provider-a", "Alpha", true),
		},
		Rules: []config.Rule{
			testRule("rule-a", "Rule A", "provider-a", "Warsaw", []string{"openai.com"}, true),
		},
		PriorityPolicies: []config.PriorityPolicy{
			{ID: "policy-a", Name: "Priority", ProviderID: "provider-a", Enabled: true, Entries: []string{"openai.com"}, Targets: []config.PriorityTarget{{Location: "Germany"}}},
		},
	}

	if err := validateActiveRuleEntries(state); err != nil {
		t.Fatalf("validateActiveRuleEntries() error = %v, want nil", err)
	}
}

func TestValidatePriorityPolicyStateRejectsMultipleEnabledPoliciesForProvider(t *testing.T) {
	state := config.State{
		Providers: []config.Provider{
			testProvider("provider-a", "Alpha", true),
		},
		PriorityPolicies: []config.PriorityPolicy{
			{ID: "policy-a", Name: "Day", ProviderID: "provider-a", Enabled: true, Targets: []config.PriorityTarget{{Location: "Germany"}}},
			{ID: "policy-b", Name: "Night", ProviderID: "provider-a", Enabled: true, Targets: []config.PriorityTarget{{Location: "Netherlands"}}},
		},
	}

	err := validatePriorityPolicyState(state)
	if err == nil {
		t.Fatal("validatePriorityPolicyState() error = nil, want duplicate provider policy error")
	}
	if !strings.Contains(err.Error(), "only one enabled priority policy") {
		t.Fatalf("validatePriorityPolicyState() error = %q, want one enabled policy message", err.Error())
	}
}

func testProvider(id, name string, enabled bool) config.Provider {
	return config.Provider{
		ID:      id,
		Name:    name,
		Type:    config.ProviderTypeSubscription,
		Source:  "https://example.com/subscription",
		Enabled: enabled,
	}
}

func testSubscriptionSource() string {
	return "{\n" + `"outbounds":[{"type":"vless","tag":"Germany","server":"127.0.0.1","server_port":443},{"type":"vless","tag":"Netherlands","server":"127.0.0.1","server_port":8443}]}`
}

func testRule(id, name, providerID, location string, domains []string, enabled bool) config.Rule {
	return config.Rule{
		ID:               id,
		Name:             name,
		ProviderID:       providerID,
		SelectedLocation: location,
		Domains:          domains,
		Enabled:          enabled,
	}
}
