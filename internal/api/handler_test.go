package api

import (
	"strings"
	"testing"

	"xiomi-router-driver/internal/config"
)

func TestValidateRuleDomainsRejectsDuplicateAcrossActiveProviders(t *testing.T) {
	providers := []config.Provider{
		testProvider("provider-a", "Alpha", true),
		testProvider("provider-b", "Beta", true),
	}
	existingRules := []config.Rule{
		testRule("rule-a", "Rule A", "provider-a", "Warsaw", []string{"chatgpt.com"}, true),
	}
	candidate := testRule("rule-b", "Rule B", "provider-b", "Prague", []string{"chatgpt.com"}, true)

	err := validateRuleDomains(candidate, providers, existingRules)
	if err == nil {
		t.Fatal("validateRuleDomains() error = nil, want duplicate error")
	}
	for _, fragment := range []string{`domain "chatgpt.com"`, `Alpha / Warsaw`, `Beta / Prague`} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("validateRuleDomains() error = %q, want fragment %q", err.Error(), fragment)
		}
	}
}

func TestValidateRuleDomainsIgnoresDisabledProviders(t *testing.T) {
	providers := []config.Provider{
		testProvider("provider-a", "Alpha", false),
		testProvider("provider-b", "Beta", true),
	}
	existingRules := []config.Rule{
		testRule("rule-a", "Rule A", "provider-a", "Warsaw", []string{"chatgpt.com"}, true),
	}
	candidate := testRule("rule-b", "Rule B", "provider-b", "Prague", []string{"chatgpt.com"}, true)

	if err := validateRuleDomains(candidate, providers, existingRules); err != nil {
		t.Fatalf("validateRuleDomains() error = %v, want nil", err)
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
	for _, fragment := range []string{`domain "youtube.com"`, `Alpha / Warsaw`, `Beta / Prague`} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("validateProviderActivation() error = %q, want fragment %q", err.Error(), fragment)
		}
	}
}

func TestValidateActiveRuleDomainsRejectsDuplicates(t *testing.T) {
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

	err := validateActiveRuleDomains(state)
	if err == nil {
		t.Fatal("validateActiveRuleDomains() error = nil, want duplicate error")
	}
	if !strings.Contains(err.Error(), `domain "oaistatic.com"`) {
		t.Fatalf("validateActiveRuleDomains() error = %q, want duplicated domain in message", err.Error())
	}
}

func TestValidateActiveRuleDomainsIgnoresDisabledRules(t *testing.T) {
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

	if err := validateActiveRuleDomains(state); err != nil {
		t.Fatalf("validateActiveRuleDomains() error = %v, want nil", err)
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
