package automation

import (
	"fmt"
	"net"
	"reflect"
	"testing"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/status"
	"xiomi-router-driver/internal/subscription"
)

func TestApplyPriorityDefaultsUsesScheduleAndDoesNotPersistPolicies(t *testing.T) {
	state := priorityTestState(t, 12001, 12002)
	at := time.Date(2026, 4, 29, 10, 30, 0, 0, time.Local)

	applied := ApplyPriorityDefaults(state, at)

	if len(applied.Rules) != 1 {
		t.Fatalf("expected one synthetic priority rule, got %+v", applied.Rules)
	}
	if applied.Rules[0].SelectedLocation != "Netherlands" {
		t.Fatalf("expected scheduled location Netherlands, got %+v", applied.Rules[0])
	}
	if want := []string{"example.com", "149.154.160.0/20"}; !reflect.DeepEqual(applied.Rules[0].Domains, want) {
		t.Fatalf("expected provider rule entries %v, got %+v", want, applied.Rules[0].Domains)
	}
	if len(applied.PriorityPolicies) != 0 {
		t.Fatalf("expected applied state to drop persisted policies, got %+v", applied.PriorityPolicies)
	}
	if len(state.PriorityPolicies) != 1 {
		t.Fatalf("source state was mutated: %+v", state.PriorityPolicies)
	}
}

func TestEvaluatePriorityPolicyFallsBackToNextHealthyTarget(t *testing.T) {
	listener := listenTCP(t)
	defer listener.Close()

	state := priorityTestState(t, 1, listenerPort(t, listener))
	supervisor := &Supervisor{dataDir: t.TempDir(), priority: newPriorityRuntime()}

	decision, ok := supervisor.evaluatePriorityPolicyLocked(t.Context(), state, status.Snapshot{}, state.PriorityPolicies[0], time.Now())
	if !ok {
		t.Fatal("evaluatePriorityPolicyLocked() ok = false")
	}
	if decision.ActiveLocation != "Netherlands" {
		t.Fatalf("expected fallback Netherlands, got %+v", decision)
	}
}

func TestEvaluatePriorityPolicyKeepsActiveTargetDuringFailureGracePeriod(t *testing.T) {
	fallback := listenTCP(t)
	defer fallback.Close()

	state := priorityTestState(t, 1, listenerPort(t, fallback))
	state.Automation.FailoverFailureSeconds = 120
	supervisor := &Supervisor{dataDir: t.TempDir(), priority: newPriorityRuntime()}
	policy := state.PriorityPolicies[0]
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.Local)
	supervisor.priority.decisions[policy.ID] = priorityDecision{
		PolicyID:       policy.ID,
		ActiveLocation: "Germany",
		Mode:           "provider",
		Fingerprint:    priorityPolicyFingerprint(policy, state),
		Since:          now.Add(-time.Hour),
	}

	decision, ok := supervisor.evaluatePriorityPolicyLocked(t.Context(), state, status.Snapshot{}, policy, now)
	if !ok {
		t.Fatal("evaluatePriorityPolicyLocked() ok = false")
	}
	if decision.ActiveLocation != "Germany" {
		t.Fatalf("expected active target to remain Germany during failure grace period, got %+v", decision)
	}
}

func TestEvaluatePriorityPolicyKeepsRunningTargetDuringStartupFailureGracePeriod(t *testing.T) {
	fallback := listenTCP(t)
	defer fallback.Close()

	state := priorityTestState(t, 1, listenerPort(t, fallback))
	state.Automation.FailoverFailureSeconds = 120
	supervisor := &Supervisor{dataDir: t.TempDir(), priority: newPriorityRuntime()}
	policy := state.PriorityPolicies[0]
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.Local)
	snapshot := status.Snapshot{
		SubscriptionRuntime: []subscription.RuntimeSnapshot{
			{
				Key:      "provider_1::germany",
				Location: "Germany",
				Status:   "running",
			},
		},
	}

	decision, ok := supervisor.evaluatePriorityPolicyLocked(t.Context(), state, snapshot, policy, now)
	if !ok {
		t.Fatal("evaluatePriorityPolicyLocked() ok = false")
	}
	if decision.ActiveLocation != "Germany" {
		t.Fatalf("expected running startup target to remain Germany during failure grace period, got %+v", decision)
	}
}

func TestEvaluatePriorityPolicyFallsBackAfterFailureGracePeriod(t *testing.T) {
	fallback := listenTCP(t)
	defer fallback.Close()
	t.Setenv("VPN_MANAGER_FAILOVER_FAILURE_STREAK", "3")

	state := priorityTestState(t, 1, listenerPort(t, fallback))
	state.Automation.FailoverFailureSeconds = 120
	supervisor := &Supervisor{dataDir: t.TempDir(), priority: newPriorityRuntime()}
	policy := state.PriorityPolicies[0]
	now := time.Date(2026, 4, 29, 12, 0, 0, 0, time.Local)
	supervisor.priority.decisions[policy.ID] = priorityDecision{
		PolicyID:       policy.ID,
		ActiveLocation: "Germany",
		Mode:           "provider",
		Fingerprint:    priorityPolicyFingerprint(policy, state),
		Since:          now.Add(-time.Hour),
	}
	supervisor.priority.health[priorityHealthKey(policy.ID, "Germany")] = providerHealthState{
		Status:              "unhealthy",
		UnhealthySince:      now.Add(-120 * time.Second),
		ConsecutiveFailures: 2,
	}

	decision, ok := supervisor.evaluatePriorityPolicyLocked(t.Context(), state, status.Snapshot{}, policy, now)
	if !ok {
		t.Fatal("evaluatePriorityPolicyLocked() ok = false")
	}
	if decision.ActiveLocation != "Netherlands" {
		t.Fatalf("expected fallback Netherlands after failure grace period, got %+v", decision)
	}
}

func TestEvaluatePriorityPolicyWaitsBeforeRestoringPreferred(t *testing.T) {
	germany := listenTCP(t)
	defer germany.Close()
	netherlands := listenTCP(t)
	defer netherlands.Close()

	state := priorityTestState(t, listenerPort(t, germany), listenerPort(t, netherlands))
	supervisor := &Supervisor{dataDir: t.TempDir(), priority: newPriorityRuntime()}
	policy := state.PriorityPolicies[0]
	supervisor.priority.decisions[policy.ID] = priorityDecision{
		PolicyID:       policy.ID,
		ActiveLocation: "Netherlands",
		Mode:           "provider",
		Fingerprint:    priorityPolicyFingerprint(policy, state),
		Since:          time.Now().Add(-time.Minute),
	}

	decision, ok := supervisor.evaluatePriorityPolicyLocked(t.Context(), state, status.Snapshot{}, policy, time.Now())
	if !ok {
		t.Fatal("evaluatePriorityPolicyLocked() ok = false")
	}
	if decision.ActiveLocation != "Netherlands" {
		t.Fatalf("expected restore delay to keep Netherlands, got %+v", decision)
	}

	supervisor.priority.health[priorityHealthKey(policy.ID, "Germany")] = providerHealthState{
		Status:               "healthy",
		HealthySince:         time.Now().Add(-2 * time.Minute),
		ConsecutiveSuccesses: failoverRestoreStreak(),
	}
	decision, ok = supervisor.evaluatePriorityPolicyLocked(t.Context(), state, status.Snapshot{}, policy, time.Now())
	if !ok {
		t.Fatal("evaluatePriorityPolicyLocked() ok = false after warm health")
	}
	if decision.ActiveLocation != "Germany" {
		t.Fatalf("expected restored Germany, got %+v", decision)
	}
}

func TestPriorityOverrideExpiresAtNextScheduleBoundary(t *testing.T) {
	policy := config.PriorityPolicy{
		ID:       "policy_1",
		Targets:  []config.PriorityTarget{{Location: "Germany"}},
		Schedule: []config.PriorityScheduleWindow{{Start: "09:00", End: "18:00", Location: "Germany"}},
	}
	since := time.Date(2026, 4, 29, 8, 30, 0, 0, time.Local)
	boundary := nextPriorityScheduleBoundary(policy, since)
	if boundary.Format("15:04") != "09:00" {
		t.Fatalf("expected next boundary 09:00, got %s", boundary.Format(time.RFC3339))
	}
}

func priorityTestState(t *testing.T, germanyPort int, netherlandsPort int) config.State {
	t.Helper()
	source := fmt.Sprintf("{\n"+`"outbounds":[{"type":"vless","tag":"Germany","server":"127.0.0.1","server_port":%d},{"type":"vless","tag":"Netherlands","server":"127.0.0.1","server_port":%d}]}`, germanyPort, netherlandsPort)
	return config.State{
		Providers: []config.Provider{
			{ID: "provider_1", Name: "Sub", Type: config.ProviderTypeSubscription, Source: source, Enabled: true},
		},
		Rules: []config.Rule{
			{ID: "rule_1", ProviderID: "provider_1", Name: "Sub / Germany", SelectedLocation: "Germany", Domains: []string{"example.com", "149.154.160.0/20"}, Enabled: true},
		},
		PriorityPolicies: []config.PriorityPolicy{
			{
				ID:         "policy_1",
				ProviderID: "provider_1",
				Name:       "Main",
				Enabled:    true,
				Targets:    []config.PriorityTarget{{Location: "Germany"}, {Location: "Netherlands"}},
				Schedule:   []config.PriorityScheduleWindow{{Start: "10:00", End: "11:00", Location: "Netherlands"}},
			},
		},
		Automation: config.DefaultAutomationSettings(),
	}
}

func listenTCP(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return listener
}

func listenerPort(t *testing.T, listener net.Listener) int {
	t.Helper()
	return listener.Addr().(*net.TCPAddr).Port
}
