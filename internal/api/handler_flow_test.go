package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/domains"
	eventstore "xiomi-router-driver/internal/events"
	"xiomi-router-driver/internal/routing"
)

func TestProviderAndRouteCRUDWorkflow(t *testing.T) {
	handler, stateManager, _, _ := newAPITestHandler(t)

	providerResp := requestJSON[struct {
		Provider config.Provider `json:"provider"`
		Count    int             `json:"count"`
	}](t, handler, http.MethodPost, "/api/providers", map[string]any{
		"name":    "FizzVPN",
		"type":    string(config.ProviderTypeSubscription),
		"source":  testSubscriptionSource(),
		"enabled": true,
	}, http.StatusCreated)
	if providerResp.Count != 1 {
		t.Fatalf("provider count = %d, want 1", providerResp.Count)
	}
	if providerResp.Provider.ID == "" || providerResp.Provider.Name != "FizzVPN" {
		t.Fatalf("unexpected provider response: %+v", providerResp.Provider)
	}

	ruleResp := requestJSON[struct {
		Rule  config.Rule `json:"rule"`
		Count int         `json:"count"`
	}](t, handler, http.MethodPost, "/api/rules", map[string]any{
		"name":             "Media route",
		"providerId":       providerResp.Provider.ID,
		"selectedLocation": "Germany",
		"domains":          "youtube.com, googlevideo.com\n149.154.160.0/20",
		"enabled":          true,
	}, http.StatusCreated)
	if ruleResp.Count != 1 {
		t.Fatalf("rule count = %d, want 1", ruleResp.Count)
	}
	if ruleResp.Rule.ID == "" || ruleResp.Rule.ProviderID != providerResp.Provider.ID {
		t.Fatalf("unexpected rule response: %+v", ruleResp.Rule)
	}
	assertStrings(t, ruleResp.Rule.Domains, []string{"youtube.com", "googlevideo.com", "149.154.160.0/20"})

	updateResp := requestJSON[struct {
		Rule config.Rule `json:"rule"`
	}](t, handler, http.MethodPut, "/api/rules/"+ruleResp.Rule.ID, map[string]any{
		"name":             "AI route",
		"providerId":       providerResp.Provider.ID,
		"selectedLocation": "Netherlands",
		"domains":          "chatgpt.com\n104.18.0.0/16",
		"enabled":          true,
	}, http.StatusOK)
	if updateResp.Rule.Name != "AI route" || updateResp.Rule.SelectedLocation != "Netherlands" {
		t.Fatalf("unexpected updated rule: %+v", updateResp.Rule)
	}
	assertStrings(t, updateResp.Rule.Domains, []string{"chatgpt.com", "104.18.0.0/16"})

	listResp := requestJSON[struct {
		Rules []config.Rule `json:"rules"`
	}](t, handler, http.MethodGet, "/api/rules", nil, http.StatusOK)
	if len(listResp.Rules) != 1 || listResp.Rules[0].ID != ruleResp.Rule.ID {
		t.Fatalf("unexpected rules list: %+v", listResp.Rules)
	}

	_ = requestJSON[map[string]string](t, handler, http.MethodDelete, "/api/rules/"+ruleResp.Rule.ID, nil, http.StatusOK)
	loaded, err := stateManager.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(loaded.Rules) != 0 {
		t.Fatalf("expected deleted route to be removed, got %+v", loaded.Rules)
	}

	_ = requestJSON[map[string]string](t, handler, http.MethodDelete, "/api/providers/"+providerResp.Provider.ID, nil, http.StatusOK)
	loaded, err = stateManager.Load()
	if err != nil {
		t.Fatalf("Load() after provider delete error = %v", err)
	}
	if len(loaded.Providers) != 0 {
		t.Fatalf("expected deleted provider to be removed, got %+v", loaded.Providers)
	}
}

func TestRouteAPIRejectsMissingProviderAndDuplicateActiveEntries(t *testing.T) {
	handler, stateManager, _, _ := newAPITestHandler(t)

	state := config.DefaultState()
	state.Providers = []config.Provider{
		testProvider("provider-a", "Alpha", true),
		testProvider("provider-b", "Beta", true),
	}
	if _, err := stateManager.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	missing := requestJSON[map[string]string](t, handler, http.MethodPost, "/api/rules", map[string]any{
		"name":       "Missing provider",
		"providerId": "provider-missing",
		"domains":    "youtube.com",
		"enabled":    true,
	}, http.StatusBadRequest)
	if !strings.Contains(missing["error"], "provider provider-missing not found") {
		t.Fatalf("unexpected missing provider error: %v", missing)
	}

	_ = requestJSON[struct {
		Rule config.Rule `json:"rule"`
	}](t, handler, http.MethodPost, "/api/rules", map[string]any{
		"name":             "Alpha video",
		"providerId":       "provider-a",
		"selectedLocation": "Germany",
		"domains":          "youtube.com",
		"enabled":          true,
	}, http.StatusCreated)

	duplicate := requestJSON[map[string]string](t, handler, http.MethodPost, "/api/rules", map[string]any{
		"name":             "Beta video",
		"providerId":       "provider-b",
		"selectedLocation": "Netherlands",
		"domains":          "youtube.com",
		"enabled":          true,
	}, http.StatusBadRequest)
	if !strings.Contains(duplicate["error"], `entry "youtube.com"`) {
		t.Fatalf("unexpected duplicate error: %v", duplicate)
	}

	loaded, err := stateManager.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(loaded.Rules) != 1 {
		t.Fatalf("expected rejected duplicate not to be saved, got %+v", loaded.Rules)
	}
}

func TestProviderDeleteRemovesDependentRoutesAndPriorityPolicies(t *testing.T) {
	handler, stateManager, _, _ := newAPITestHandler(t)

	state := config.DefaultState()
	state.Providers = []config.Provider{testProvider("provider-a", "Alpha", true)}
	state.Rules = []config.Rule{
		testRule("rule-a", "Alpha route", "provider-a", "Germany", []string{"openai.com"}, true),
	}
	state.PriorityPolicies = []config.PriorityPolicy{
		{
			ID:         "policy-a",
			Name:       "Alpha priority",
			ProviderID: "provider-a",
			Enabled:    true,
			Targets:    []config.PriorityTarget{{Location: "Germany"}},
			Entries:    []string{"openai.com"},
		},
	}
	if _, err := stateManager.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	_ = requestJSON[map[string]string](t, handler, http.MethodDelete, "/api/providers/provider-a", nil, http.StatusOK)

	loaded, err := stateManager.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(loaded.Providers) != 0 || len(loaded.Rules) != 0 || len(loaded.PriorityPolicies) != 0 {
		t.Fatalf("expected provider delete to cascade, got providers=%+v rules=%+v policies=%+v", loaded.Providers, loaded.Rules, loaded.PriorityPolicies)
	}
}

func TestSubscriptionRefreshBypassesCacheAndRecordsEvent(t *testing.T) {
	handler, stateManager, _, eventsStore := newAPITestHandler(t)
	source := mutableSubscriptionSource(t, []string{"Germany"})

	state := config.DefaultState()
	state.Providers = []config.Provider{
		{
			ID:      "provider-sub",
			Name:    "FizzVPN",
			Type:    config.ProviderTypeSubscription,
			Source:  source.URL,
			Enabled: true,
		},
	}
	if _, err := stateManager.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	first := requestJSON[struct {
		Status  string `json:"status"`
		Entries int    `json:"entries"`
		Applied bool   `json:"applied"`
	}](t, handler, http.MethodPost, "/api/providers/provider-sub/refresh", nil, http.StatusOK)
	if first.Status != "refreshed" || first.Entries != 1 || first.Applied {
		t.Fatalf("unexpected first refresh response: %+v", first)
	}

	source.Set([]string{"Germany", "Netherlands", "Japan"})
	second := requestJSON[struct {
		Status  string `json:"status"`
		Entries int    `json:"entries"`
		Applied bool   `json:"applied"`
	}](t, handler, http.MethodPost, "/api/providers/provider-sub/refresh", nil, http.StatusOK)
	if second.Status != "refreshed" || second.Entries != 3 || second.Applied {
		t.Fatalf("refresh did not bypass the fresh cache: %+v", second)
	}

	list, total, err := eventsStore.ListByLevel("info", 10, 0)
	if err != nil {
		t.Fatalf("ListByLevel() error = %v", err)
	}
	if total == 0 {
		t.Fatalf("expected refresh to record an info event")
	}
	found := false
	for _, event := range list {
		if event.Kind == "subscription.refreshed" && strings.Contains(event.Message, "FizzVPN") && strings.Contains(event.Message, "3 entries") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected latest refresh event with new entry count, got %+v", list)
	}
}

func TestSubscriptionRefreshWithActiveRouteRequiresRuntimeApply(t *testing.T) {
	handler, stateManager, _, _ := newAPITestHandler(t)

	state := config.DefaultState()
	state.Providers = []config.Provider{
		{
			ID:      "provider-sub",
			Name:    "FizzVPN",
			Type:    config.ProviderTypeSubscription,
			Source:  testSubscriptionSource(),
			Enabled: true,
		},
	}
	state.Rules = []config.Rule{
		testRule("rule-sub", "AI route", "provider-sub", "Germany", []string{"chatgpt.com"}, true),
	}
	if _, err := stateManager.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	response := requestJSON[map[string]string](t, handler, http.MethodPost, "/api/providers/provider-sub/refresh", nil, http.StatusInternalServerError)
	if !strings.Contains(response["error"], "subscription runtime manager is not configured") {
		t.Fatalf("unexpected active refresh error: %v", response)
	}

	loaded, err := stateManager.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.LastError == "" || !strings.Contains(loaded.LastError, "subscription runtime manager is not configured") {
		t.Fatalf("expected failed active refresh to persist LastError, got %q", loaded.LastError)
	}
}

func TestManualApplyWithActiveSubscriptionRequiresRuntimeAndKeepsPreviousDomains(t *testing.T) {
	handler, stateManager, domainsManager, _ := newAPITestHandler(t)
	if err := domainsManager.ReplaceAll([]string{"previous.example.com"}); err != nil {
		t.Fatalf("ReplaceAll() error = %v", err)
	}

	state := config.DefaultState()
	state.Providers = []config.Provider{
		testProvider("provider-a", "Alpha", true),
	}
	state.Rules = []config.Rule{
		testRule("rule-a", "Alpha route", "provider-a", "Germany", []string{"chatgpt.com", "openai.com"}, true),
	}
	if _, err := stateManager.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	response := requestJSON[map[string]string](t, handler, http.MethodPost, "/api/rules/apply", nil, http.StatusInternalServerError)
	if !strings.Contains(response["error"], "subscription runtime manager is not configured") {
		t.Fatalf("unexpected apply error: %v", response)
	}

	appliedDomains, err := domainsManager.List()
	if err != nil {
		t.Fatalf("domains List() error = %v", err)
	}
	assertStrings(t, appliedDomains, []string{"previous.example.com"})

	loaded, err := stateManager.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.LastAppliedAt != "" {
		t.Fatalf("expected LastAppliedAt to stay empty after failed apply, got %q", loaded.LastAppliedAt)
	}
	if loaded.LastError == "" || !strings.Contains(loaded.LastError, "subscription runtime manager is not configured") {
		t.Fatalf("expected LastError to describe missing subscription runtime, got %q", loaded.LastError)
	}
}

type mutableSubscription struct {
	URL string
	set func([]string)
}

func (m mutableSubscription) Set(locations []string) {
	m.set(locations)
}

func mutableSubscriptionSource(t *testing.T, locations []string) mutableSubscription {
	t.Helper()

	current := append([]string(nil), locations...)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(singBoxSubscriptionPayload(current))
	}))
	t.Cleanup(server.Close)

	return mutableSubscription{
		URL: server.URL,
		set: func(next []string) {
			current = append([]string(nil), next...)
		},
	}
}

func singBoxSubscriptionPayload(locations []string) map[string]any {
	outbounds := make([]map[string]any, 0, len(locations))
	for index, location := range locations {
		outbounds = append(outbounds, map[string]any{
			"type":        "vless",
			"tag":         location,
			"server":      "127.0.0.1",
			"server_port": 443 + index,
		})
	}
	return map[string]any{"outbounds": outbounds}
}

func newAPITestHandler(t *testing.T) (*Handler, *config.Manager, *domains.Manager, *eventstore.Store) {
	t.Helper()

	tempDir := t.TempDir()
	db := openAPITestDB(t, filepath.Join(tempDir, "vpn-manager.db"))
	stateManager := config.NewManager(db, filepath.Join(tempDir, "vpn-state.json"))
	domainsManager := domains.NewManager(db, filepath.Join(tempDir, "domains.current"), filepath.Join(tempDir, "domains.legacy"))
	eventsStore := eventstore.NewStore(db, filepath.Join(tempDir, "events.json"))
	routingRunner := routing.NewRunner(writeNoopRoutingScript(t, tempDir))
	handler := NewHandler(Dependencies{
		State:   stateManager,
		Domains: domainsManager,
		Events:  eventsStore,
		Routing: routingRunner,
		DataDir: tempDir,
	})
	return handler, stateManager, domainsManager, eventsStore
}

func writeNoopRoutingScript(t *testing.T, dir string) string {
	t.Helper()

	path := filepath.Join(dir, "update_routes.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func requestJSON[T any](t *testing.T, handler *Handler, method string, path string, body any, wantStatus int) T {
	t.Helper()

	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
		reader = bytes.NewReader(payload)
	}

	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != wantStatus {
		t.Fatalf("%s %s status = %d, want %d, body=%s", method, path, rec.Code, wantStatus, rec.Body.String())
	}

	var decoded T
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %s %s response: %v; body=%s", method, path, err, rec.Body.String())
	}
	return decoded
}

func assertStrings(t *testing.T, got []string, want []string) {
	t.Helper()

	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("strings = %q, want %q", got, want)
	}
}
