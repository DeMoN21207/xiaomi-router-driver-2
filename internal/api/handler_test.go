package api

import (
	"strings"
	"testing"

	"xiomi-router-driver/internal/config"
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

func testProvider(id, name string, enabled bool) config.Provider {
	return config.Provider{
		ID:      id,
		Name:    name,
		Type:    config.ProviderTypeSubscription,
		Source:  "https://example.com/subscription",
		Enabled: enabled,
	}
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
