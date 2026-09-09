package automation

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/status"
	"xiomi-router-driver/internal/subscription"
)

func TestRecoveryIntervalTracksFailureThreshold(t *testing.T) {
	s := &Supervisor{interval: 20 * time.Second}
	state := config.DefaultState()
	state.Automation.FailoverFailureSeconds = 15
	if got := s.recoveryInterval(state); got != 5*time.Second {
		t.Fatalf("15s failover needs 5s checks, got %v", got)
	}
	state.Automation.FailoverFailureSeconds = 120
	if got := s.recoveryInterval(state); got != 20*time.Second {
		t.Fatalf("ordinary interval changed: %v", got)
	}
	s.interval = time.Second
	if got := s.recoveryInterval(state); got != time.Second {
		t.Fatalf("explicit faster interval ignored: %v", got)
	}
}

func TestPriorityFailsOverAfter15SecondsOfTunnelFailure(t *testing.T) {
	primary, fallback := listenTCP(t), listenTCP(t)
	defer primary.Close()
	defer fallback.Close()
	state := priorityTestState(t, listenerPort(t, primary), listenerPort(t, fallback))
	state.Automation.FailoverFailureSeconds = 15
	t.Setenv("VPN_MANAGER_FAILOVER_FAILURE_STREAK", "3")
	policy := state.PriorityPolicies[0]
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	s := &Supervisor{dataDir: t.TempDir(), priority: newPriorityRuntime(),
		probeTunnel: func(context.Context, string, string) providerProbeResult {
			return providerProbeResult{Detail: "HTTPS through tunnel timed out"}
		},
	}
	s.priority.decisions[policy.ID] = priorityDecision{ActiveLocation: "Germany", Mode: "provider", Since: now.Add(-time.Hour)}
	snapshot := status.Snapshot{SubscriptionRuntime: []subscription.RuntimeSnapshot{{
		Key: "provider_1::germany", ProviderID: "provider_1", Location: "Germany", Status: "running", InterfaceName: "test-tun", FWMark: "0x1",
	}}}
	for _, seconds := range []int{0, 5, 10, 15} {
		decision, ok := s.evaluatePriorityPolicyLocked(t.Context(), state, snapshot, policy, now.Add(time.Duration(seconds)*time.Second))
		want := "Germany"
		if seconds == 15 {
			want = "Netherlands"
		}
		if !ok || decision.ActiveLocation != want {
			t.Fatalf("at %ds got %+v, want %s", seconds, decision, want)
		}
	}
}

func TestTunnelHTTPProbeNeedsOnlyOneWorkingDestination(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer good.Close()
	if got := probeTunnelURLs(t.Context(), good.Client(), []string{bad.URL, good.URL}); !got.Healthy {
		t.Fatalf("one healthy destination should suffice: %+v", got)
	}
	if got := probeTunnelURLs(t.Context(), bad.Client(), []string{bad.URL}); got.Healthy {
		t.Fatalf("503 must not count as healthy: %+v", got)
	}
}

func TestTunnelHTTPProbeHonorsDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	if got := probeTunnelURLs(ctx, server.Client(), []string{server.URL}); got.Healthy {
		t.Fatalf("timed out probe is healthy: %+v", got)
	}
	if time.Since(start) > time.Second {
		t.Fatal("probe ignored deadline")
	}
}

func TestPriorityKeepsHealthyFallbackAndVerifiesPrimaryThroughVPN(t *testing.T) {
	state := priorityTestState(t, 1, 2)
	policy := state.PriorityPolicies[0]
	policy.Targets = append(policy.Targets, config.PriorityTarget{Location: "Sweden"})
	state.PriorityPolicies[0] = policy
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	s := &Supervisor{priority: newPriorityRuntime(),
		probeTunnel: func(context.Context, string, string) providerProbeResult { return providerProbeResult{Healthy: true} },
		probeStandby: func(_ context.Context, _ config.Provider, location string) providerProbeResult {
			if location == "Netherlands" {
				t.Error("working fallback should not be displaced by intermediate target")
			}
			return providerProbeResult{Detail: "endpoint accepts TCP but VPN has no internet"}
		},
	}
	s.priority.decisions[policy.ID] = priorityDecision{ActiveLocation: "Sweden", Mode: "provider", Since: now.Add(-time.Hour)}
	snapshot := status.Snapshot{SubscriptionRuntime: []subscription.RuntimeSnapshot{{
		Key: "provider_1::sweden", ProviderID: "provider_1", Location: "Sweden", Status: "running", InterfaceName: "test-tun",
	}}}
	for i := 0; i <= 90; i += 5 {
		decision, _ := s.evaluatePriorityPolicyLocked(t.Context(), state, snapshot, policy, now.Add(time.Duration(i)*time.Second))
		if decision.ActiveLocation != "Sweden" {
			t.Fatalf("at %ds switched to broken target: %+v", i, decision)
		}
	}
}
