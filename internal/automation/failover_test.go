package automation

import (
	"context"
	"strings"
	"testing"
	"time"

	"xiomi-router-driver/internal/config"
)

func TestPingHostViaInterfaceReportsMissingInterface(t *testing.T) {
	result := pingHostViaInterfaceForFailover(t.Context(), "1.1.1.1", "definitely-missing-sb0")

	if result.Healthy {
		t.Fatal("pingHostViaInterfaceForFailover() healthy = true, want false")
	}
	if !strings.Contains(result.Detail, "interface is missing") {
		t.Fatalf("detail = %q, want missing interface detail", result.Detail)
	}
}

func TestApplyAllDownPolicyDoesNotRepeatSameFailureEvent(t *testing.T) {
	var events []string
	supervisor := &Supervisor{
		failover: newFailoverRuntime(),
		recordEvent: func(level string, kind string, message string) {
			events = append(events, level+"|"+kind+"|"+message)
		},
	}
	provider := config.Provider{ID: "provider_1", Name: "FizzVPN"}
	rules := []config.Rule{{ID: "rule_1"}}

	if !supervisor.applyAllDownPolicy(context.Background(), config.State{}, provider, rules, "lookup timeout") {
		t.Fatal("applyAllDownPolicy() = false")
	}
	if !supervisor.applyAllDownPolicy(context.Background(), config.State{}, provider, rules, "lookup timeout") {
		t.Fatal("applyAllDownPolicy() second call = false")
	}
	if len(events) != 1 {
		t.Fatalf("events after duplicate failure = %d, want 1: %#v", len(events), events)
	}

	supervisor.updateProviderHealth(provider, providerProbeResult{Healthy: true}, time.Now())
	if !supervisor.applyAllDownPolicy(context.Background(), config.State{}, provider, rules, "lookup timeout") {
		t.Fatal("applyAllDownPolicy() after recovery = false")
	}
	if len(events) != 2 {
		t.Fatalf("events after recovery failure = %d, want 2: %#v", len(events), events)
	}
}

func TestApplyAllDownPolicyReleasesAffectedRulesInDirectMode(t *testing.T) {
	var applied config.State
	supervisor := &Supervisor{
		failover: newFailoverRuntime(),
		applyState: func(_ context.Context, state config.State) error {
			applied = state
			return nil
		},
	}
	provider := config.Provider{ID: "provider_1", Name: "FizzVPN", Enabled: true}
	rule := config.Rule{ID: "rule_1", ProviderID: provider.ID, Enabled: true, Domains: []string{"example.com"}}
	state := config.State{
		Providers:  []config.Provider{provider},
		Rules:      []config.Rule{rule},
		Automation: config.AutomationSettings{FailoverAllDownMode: "direct"},
	}

	if !supervisor.applyAllDownPolicy(context.Background(), state, provider, []config.Rule{rule}, "all probes failed") {
		t.Fatal("applyAllDownPolicy() = false")
	}
	if len(applied.Rules) != 1 || applied.Rules[0].Enabled {
		t.Fatalf("direct all-down state did not disable affected rule: %+v", applied.Rules)
	}
	override, ok := supervisor.failover.overrides[rule.ID]
	if !ok || override.Mode != "direct" {
		t.Fatalf("direct failover override was not recorded: %+v", supervisor.failover.overrides)
	}
}
