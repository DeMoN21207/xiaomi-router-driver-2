package automation

import (
	"context"
	"testing"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/status"
	"xiomi-router-driver/internal/subscription"
)

func TestPriorityManualSelectionChecksHealthWithoutAutomaticRestoreDelay(t *testing.T) {
	for _, healthy := range []bool{true, false} {
		name := "unhealthy target keeps current VPN"
		if healthy {
			name = "healthy target switches immediately"
		}
		t.Run(name, func(t *testing.T) {
			state := priorityTestState(t, 1, 2)
			policy := state.PriorityPolicies[0]
			now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
			s := &Supervisor{priority: newPriorityRuntime(),
				probeTunnel: func(context.Context, string, string) providerProbeResult {
					return providerProbeResult{Healthy: true}
				},
				probeStandby: func(_ context.Context, _ config.Provider, location string) providerProbeResult {
					if location != "Netherlands" {
						t.Errorf("unexpected standby target: %s", location)
					}
					return providerProbeResult{Healthy: healthy}
				},
			}
			s.priority.decisions[policy.ID] = priorityDecision{ActiveLocation: "Germany", Mode: "provider", Since: now.Add(-time.Hour)}
			if err := s.SetPriorityOverride(policy.ID, "Netherlands"); err != nil {
				t.Fatal(err)
			}
			snapshot := status.Snapshot{SubscriptionRuntime: []subscription.RuntimeSnapshot{{
				Key: "provider_1::germany", ProviderID: "provider_1", Location: "Germany", Status: "running", InterfaceName: "test-tun",
			}}}
			decision, ok := s.evaluatePriorityPolicyLocked(t.Context(), state, snapshot, policy, now)
			want := "Germany"
			if healthy {
				want = "Netherlands"
			}
			if !ok || decision.ActiveLocation != want {
				t.Fatalf("manual selection got %+v, want %s", decision, want)
			}
			if _, kept := s.priority.overrides[policy.ID]; kept != healthy {
				t.Fatalf("manual selection retained = %v, want %v", kept, healthy)
			}
		})
	}
}

func TestPriorityFailedBackupReturnsToWorkingPrimaryAfterFailureGrace(t *testing.T) {
	t.Setenv("VPN_MANAGER_FAILOVER_FAILURE_STREAK", "3")
	state := priorityTestState(t, 1, 2)
	state.Automation.FailoverFailureSeconds = 15
	state.Automation.FailoverRestoreSeconds = 60
	policy := state.PriorityPolicies[0]
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	s := &Supervisor{priority: newPriorityRuntime(),
		probeTunnel: func(context.Context, string, string) providerProbeResult {
			return providerProbeResult{Detail: "active backup has no internet"}
		},
		probeStandby: func(context.Context, config.Provider, string) providerProbeResult {
			return providerProbeResult{Healthy: true}
		},
	}
	s.priority.decisions[policy.ID] = priorityDecision{ActiveLocation: "Netherlands", Mode: "provider", Since: now.Add(-time.Hour)}
	snapshot := status.Snapshot{SubscriptionRuntime: []subscription.RuntimeSnapshot{{
		Key: "provider_1::netherlands", ProviderID: "provider_1", Location: "Netherlands", Status: "running", InterfaceName: "test-tun",
	}}}
	for _, seconds := range []int{0, 5, 10, 15} {
		decision, ok := s.evaluatePriorityPolicyLocked(t.Context(), state, snapshot, policy, now.Add(time.Duration(seconds)*time.Second))
		want := "Netherlands"
		if seconds == 15 {
			want = "Germany"
		}
		if !ok || decision.ActiveLocation != want {
			t.Fatalf("at %ds got %+v, want %s", seconds, decision, want)
		}
	}
}
