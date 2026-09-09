package automation

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/sqlitedb"
	"xiomi-router-driver/internal/status"
	"xiomi-router-driver/internal/subscription"
)

func TestPriorityStatusRemainsResponsiveDuringApply(t *testing.T) {
	for _, applyFails := range []bool{false, true} {
		name := "success publishes only after completion"
		if applyFails {
			name = "failure preserves last successful decision"
		}
		t.Run(name, func(t *testing.T) {
			s, state, snapshot := priorityConcurrencySupervisor(t)
			before := s.PriorityStatus()
			if err := s.SetPriorityOverride("policy_1", "Netherlands"); err != nil {
				t.Fatal(err)
			}
			started := make(chan config.State, 1)
			release := make(chan struct{})
			s.applyState = func(_ context.Context, applied config.State) error {
				started <- applied
				<-release
				if applyFails {
					return errors.New("route update failed")
				}
				return nil
			}
			done := make(chan bool, 1)
			go func() { done <- s.maybePriorityPolicies(t.Context(), state, snapshot) }()
			var releaseOnce sync.Once
			defer func() {
				releaseOnce.Do(func() { close(release) })
				priorityAwait(t, done)
			}()
			applied := priorityAwait(t, started)
			if got := applied.Rules[0].SelectedLocation; got != "Netherlands" {
				t.Fatalf("apply target = %q, want Netherlands", got)
			}

			during := priorityStatusPromptly(t, s)
			if during.Policies[0].ActiveLocation != "Germany" || during.LastApplyAt != before.LastApplyAt {
				t.Fatalf("unfinished apply published attempted target: before=%+v during=%+v", before, during)
			}
			appliedRead := make(chan config.State, 1)
			go func() { appliedRead <- s.priorityAppliedState(state) }()
			if got := priorityAwait(t, appliedRead).Rules[0].SelectedLocation; got != "Germany" {
				t.Fatalf("unfinished apply used attempted target in routing state: %q", got)
			}

			releaseOnce.Do(func() { close(release) })
			changed := priorityAwait(t, done)
			done <- changed // Let the deferred cleanup also observe completion.
			after := priorityStatusPromptly(t, s)
			if applyFails {
				if changed || after.Policies[0].ActiveLocation != "Germany" || after.LastApplyAt != before.LastApplyAt {
					t.Fatalf("failed apply changed published state: changed=%v status=%+v", changed, after)
				}
			} else if !changed || after.Policies[0].ActiveLocation != "Netherlands" || after.LastApplyAt == before.LastApplyAt {
				t.Fatalf("successful apply was not published: changed=%v status=%+v", changed, after)
			}
		})
	}
}

func TestPriorityManualOverrideDuringApplyIsRetained(t *testing.T) {
	for _, clearOverride := range []bool{false, true} {
		name := "set"
		if clearOverride {
			name = "clear"
		}
		t.Run(name, func(t *testing.T) {
			s, state, snapshot := priorityConcurrencySupervisor(t)
			if err := s.SetPriorityOverride("policy_1", "Netherlands"); err != nil {
				t.Fatal(err)
			}
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			s.applyState = func(context.Context, config.State) error {
				started <- struct{}{}
				<-release
				return nil
			}
			done := make(chan bool, 1)
			go func() { done <- s.maybePriorityPolicies(t.Context(), state, snapshot) }()
			var releaseOnce sync.Once
			defer func() {
				releaseOnce.Do(func() { close(release) })
				priorityAwait(t, done)
			}()
			priorityAwait(t, started)
			updated := make(chan error, 1)
			go func() {
				if clearOverride {
					s.ClearPriorityOverride("policy_1")
					updated <- nil
					return
				}
				updated <- s.SetPriorityOverride("policy_1", "Germany")
			}()
			if err := priorityAwait(t, updated); err != nil {
				t.Fatal(err)
			}
			releaseOnce.Do(func() { close(release) })
			changed := priorityAwait(t, done)
			done <- changed
			want := "Germany"
			if clearOverride {
				want = ""
			}
			if got := priorityStatusPromptly(t, s).Policies[0].OverrideLocation; got != want {
				t.Fatalf("override after pending apply = %q, want %q", got, want)
			}
		})
	}
}

func TestPriorityStatusAndNewOverrideRemainAvailableDuringProbe(t *testing.T) {
	s, state, snapshot := priorityConcurrencySupervisor(t)
	if err := s.SetPriorityOverride("policy_1", "Netherlands"); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	s.probeStandby = func(context.Context, config.Provider, string) providerProbeResult {
		started <- struct{}{}
		<-release
		return providerProbeResult{Healthy: false, Detail: "manual target unavailable"}
	}
	done := make(chan bool, 1)
	go func() { done <- s.maybePriorityPolicies(t.Context(), state, snapshot) }()
	var releaseOnce sync.Once
	defer func() {
		releaseOnce.Do(func() { close(release) })
		priorityAwait(t, done)
	}()
	priorityAwait(t, started)
	if got := priorityStatusPromptly(t, s).Policies[0].ActiveLocation; got != "Germany" {
		t.Fatalf("active location while probing = %q, want Germany", got)
	}
	updated := make(chan error, 1)
	go func() { updated <- s.SetPriorityOverride("policy_1", "Germany") }()
	if err := priorityAwait(t, updated); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	changed := priorityAwait(t, done)
	done <- changed
	if got := priorityStatusPromptly(t, s).Policies[0].OverrideLocation; got != "Germany" {
		t.Fatalf("failed probe removed newer manual selection: override=%q", got)
	}
}

func TestPriorityEvaluationsSerializeAndUseLatestOverride(t *testing.T) {
	s, state, snapshot := priorityConcurrencySupervisor(t)
	if err := s.SetPriorityOverride("policy_1", "Netherlands"); err != nil {
		t.Fatal(err)
	}
	started := make(chan string, 2)
	release := make(chan struct{})
	s.applyState = func(_ context.Context, applied config.State) error {
		started <- applied.Rules[0].SelectedLocation
		<-release
		return nil
	}
	firstDone := make(chan bool, 1)
	secondDone := make(chan bool, 1)
	go func() { firstDone <- s.maybePriorityPolicies(t.Context(), state, snapshot) }()
	var releaseOnce sync.Once
	defer func() {
		releaseOnce.Do(func() { close(release) })
		priorityAwait(t, firstDone)
	}()
	if got := priorityAwait(t, started); got != "Netherlands" {
		t.Fatalf("first apply = %q, want Netherlands", got)
	}
	updated := make(chan error, 1)
	go func() { updated <- s.SetPriorityOverride("policy_1", "Germany") }()
	if err := priorityAwait(t, updated); err != nil {
		t.Fatal(err)
	}
	go func() { secondDone <- s.maybePriorityPolicies(t.Context(), state, snapshot) }()
	defer func() {
		releaseOnce.Do(func() { close(release) })
		priorityAwait(t, secondDone)
	}()
	select {
	case got := <-started:
		t.Fatalf("second apply started before first completed: %q", got)
	case <-time.After(100 * time.Millisecond):
	}
	releaseOnce.Do(func() { close(release) })
	if got := priorityAwait(t, started); got != "Germany" {
		t.Fatalf("queued evaluation ignored new selection: %q", got)
	}
	first := priorityAwait(t, firstDone)
	second := priorityAwait(t, secondDone)
	firstDone <- first
	secondDone <- second
	after := priorityStatusPromptly(t, s)
	if !first || !second || after.Policies[0].ActiveLocation != "Germany" || after.Policies[0].OverrideLocation != "Germany" {
		t.Fatalf("latest decision was overwritten: first=%v second=%v status=%+v", first, second, after)
	}
}

func priorityConcurrencySupervisor(t *testing.T) (*Supervisor, config.State, status.Snapshot) {
	t.Helper()
	state := priorityTestState(t, 1, 2)
	state.PriorityPolicies[0].Schedule = nil
	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "priority.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	manager := config.NewManager(db, "")
	state, err = manager.Save(state)
	if err != nil {
		t.Fatal(err)
	}
	s := &Supervisor{
		state: manager, priority: newPriorityRuntime(),
		applyState: func(context.Context, config.State) error { return nil },
		probeTunnel: func(context.Context, string, string) providerProbeResult {
			return providerProbeResult{Healthy: true}
		},
		probeStandby: func(context.Context, config.Provider, string) providerProbeResult {
			return providerProbeResult{Healthy: true}
		},
	}
	policy := state.PriorityPolicies[0]
	s.priority.lastApply = time.Now().Add(-time.Hour)
	s.priority.decisions[policy.ID] = priorityDecision{
		PolicyID: policy.ID, PolicyName: policy.Name, ProviderID: policy.ProviderID,
		ActiveLocation: "Germany", PreferredLocation: "Germany", Mode: "provider",
		Since: s.priority.lastApply, Fingerprint: priorityPolicyFingerprint(policy, state),
	}
	snapshot := status.Snapshot{
		WAN: status.WANStatus{State: "up"},
		SubscriptionRuntime: []subscription.RuntimeSnapshot{{
			Key: "provider_1::germany", ProviderID: "provider_1", Location: "Germany",
			Status: "running", InterfaceName: "test-tun",
		}},
	}
	return s, state, snapshot
}

func priorityStatusPromptly(t *testing.T, s *Supervisor) PriorityStatus {
	t.Helper()
	result := make(chan PriorityStatus, 1)
	go func() { result <- s.PriorityStatus() }()
	return priorityAwait(t, result)
}

func priorityAwait[T any](t *testing.T, result <-chan T) T {
	t.Helper()
	select {
	case value := <-result:
		return value
	case <-time.After(time.Second):
		t.Fatal("priority operation blocked while an apply or probe was in progress")
		var zero T
		return zero
	}
}
