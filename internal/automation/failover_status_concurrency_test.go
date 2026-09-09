package automation

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/status"
)

func TestFailoverStatusRemainsResponsiveDuringApply(t *testing.T) {
	for _, applyFails := range []bool{false, true} {
		name := "success publishes completed override"
		if applyFails {
			name = "failure preserves successful runtime"
		}
		t.Run(name, func(t *testing.T) {
			s, state, snapshot := failoverConcurrencySupervisor(t)
			previousApply := timeString(s.failover.lastApply)
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			s.applyState = func(_ context.Context, applied config.State) error {
				if applied.Rules[0].ProviderID != "provider_2" {
					t.Errorf("apply provider = %q, want provider_2", applied.Rules[0].ProviderID)
				}
				started <- struct{}{}
				<-release
				if applyFails {
					return errors.New("route update failed")
				}
				return nil
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				s.maybeProviderFailover(t.Context(), state, snapshot)
			}()
			var releaseOnce sync.Once
			defer func() {
				releaseOnce.Do(func() { close(release) })
				priorityAwait(t, done)
			}()
			priorityAwait(t, started)
			// The first status read occurs while application owns failoverMu.
			// It must retain the prior completed runtime, not attempted overrides.
			during := failoverStatusPromptly(t, s)
			if len(during.ActiveOverrides) != 0 || during.LastApplyAt != previousApply {
				t.Fatalf("pending failover published attempted routes: %+v", during)
			}
			releaseOnce.Do(func() { close(release) })
			priorityAwait(t, done)
			after := publishedFailoverStatusPromptly(t, s)
			if applyFails {
				if len(after.ActiveOverrides) != 0 || after.LastApplyAt != previousApply {
					t.Fatalf("failed failover changed published runtime: %+v", after)
				}
			} else if len(after.ActiveOverrides) != 1 || after.ActiveOverrides[0].ActiveProviderID != "provider_2" || after.LastApplyAt == previousApply {
				t.Fatalf("completed failover was not published: %+v", after)
			}
		})
	}
}

func TestFailoverStatusRemainsResponsiveDuringProbe(t *testing.T) {
	s, state, snapshot := failoverConcurrencySupervisor(t)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	s.probeTunnel = func(context.Context, string, string) providerProbeResult {
		started <- struct{}{}
		<-release
		return providerProbeResult{Healthy: true}
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.maybeProviderFailover(t.Context(), state, snapshot)
	}()
	var releaseOnce sync.Once
	defer func() {
		releaseOnce.Do(func() { close(release) })
		priorityAwait(t, done)
	}()
	priorityAwait(t, started)
	during := failoverStatusPromptly(t, s)
	if during.Providers[0].Status != "unhealthy" {
		t.Fatalf("pending probe changed provider health: %+v", during)
	}
	releaseOnce.Do(func() { close(release) })
	priorityAwait(t, done)
	after := publishedFailoverStatusPromptly(t, s)
	if after.Providers[0].Status != "healthy" || after.Providers[0].ConsecutiveFailures != 0 {
		t.Fatalf("completed probe was not published: %+v", after)
	}
}

func failoverConcurrencySupervisor(t *testing.T) (*Supervisor, config.State, status.Snapshot) {
	t.Helper()
	t.Setenv("VPN_MANAGER_FAILOVER_FAILURE_STREAK", "3")
	s, state, snapshot := priorityConcurrencySupervisor(t)
	listener := listenTCP(t)
	t.Cleanup(func() { _ = listener.Close() })
	port := listenerPort(t, listener)
	state.Providers = append(state.Providers, config.Provider{
		ID: "provider_2", Name: "Backup", Type: config.ProviderTypeSubscription,
		Source: priorityTestState(t, port, port).Providers[0].Source, Enabled: true,
	})
	state.PriorityPolicies = nil
	state.Automation.ProviderFailover = true
	state.Automation.FailoverFailureSeconds = 1
	state, err := s.state.Save(state)
	if err != nil {
		t.Fatal(err)
	}
	s.failover = newFailoverRuntime()
	s.failover.lastApply = time.Now().Add(-time.Hour)
	s.failover.health["provider_1"] = providerHealthState{
		ProviderID: "provider_1", ProviderName: "Sub", Status: "unhealthy",
		ConsecutiveFailures: 3, UnhealthySince: time.Now().Add(-time.Hour),
	}
	s.probeTunnel = func(context.Context, string, string) providerProbeResult {
		return providerProbeResult{Healthy: false, Detail: "active tunnel unavailable"}
	}
	return s, state, snapshot
}

func failoverStatusPromptly(t *testing.T, s *Supervisor) FailoverStatus {
	t.Helper()
	result := make(chan FailoverStatus, 1)
	go func() { result <- s.FailoverStatus() }()
	return priorityAwait(t, result)
}

func publishedFailoverStatusPromptly(t *testing.T, s *Supervisor) FailoverStatus {
	t.Helper()
	// Require the completed writer to have published its result, even when a
	// subsequent writer already owns the runtime and a fresh copy is unavailable.
	s.failoverMu.Lock()
	defer s.failoverMu.Unlock()
	return failoverStatusPromptly(t, s)
}
