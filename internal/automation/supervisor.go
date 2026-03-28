package automation

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/openvpn"
	"xiomi-router-driver/internal/status"
	"xiomi-router-driver/internal/subscription"
)

type ApplyFunc func(ctx context.Context) error

type Supervisor struct {
	state        *config.Manager
	status       *status.Service
	apply        ApplyFunc
	recordEvent  func(level string, kind string, message string)
	interval     time.Duration
	lastWAN      string
	lastCleanup  time.Time
}

func NewSupervisor(
	state *config.Manager,
	statusService *status.Service,
	apply ApplyFunc,
	recordEvent func(level string, kind string, message string),
) *Supervisor {
	interval := 20 * time.Second
	if raw := strings.TrimSpace(os.Getenv("VPN_MANAGER_RECOVERY_INTERVAL")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			interval = parsed
		}
	}

	return &Supervisor{
		state:       state,
		status:      statusService,
		apply:       apply,
		recordEvent: recordEvent,
		interval:    interval,
	}
}

func (s *Supervisor) Run(ctx context.Context) {
	if s.state == nil || s.status == nil || s.apply == nil {
		return
	}

	s.startup(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Supervisor) startup(ctx context.Context) {
	if snapshot, err := s.status.Snapshot(ctx); err == nil {
		s.lastWAN = snapshot.WAN.State
	}

	state, err := s.state.Load()
	if err != nil {
		s.record("error", "automation.reconcile_failed", fmt.Sprintf("load state on startup: %v", err))
		return
	}
	if !state.Automation.InstallService {
		return
	}
	if !hasEnabledRules(state) {
		return
	}
	if err := s.apply(ctx); err != nil {
		s.record("error", "automation.reconcile_failed", fmt.Sprintf("startup restore failed: %v", err))
		log.Printf("automation startup restore failed: %v", err)
		return
	}
	s.record("info", "automation.reconcile_restored", "Saved routes restored on startup")
}

func (s *Supervisor) tick(ctx context.Context) {
	state, err := s.state.Load()
	if err != nil {
		s.record("error", "automation.reconcile_failed", fmt.Sprintf("load state: %v", err))
		return
	}

	// Periodic traffic cleanup (run at most once per hour).
	s.maybeCleanupTraffic(state)

	snapshot, err := s.status.Snapshot(ctx)
	if err != nil {
		s.record("error", "automation.reconcile_failed", fmt.Sprintf("load status snapshot: %v", err))
		return
	}

	previousWAN := s.lastWAN
	s.lastWAN = snapshot.WAN.State
	if previousWAN == "" {
		return
	}

	if !state.Automation.AutoRecover || !hasEnabledRules(state) {
		return
	}

	if previousWAN != "up" && snapshot.WAN.State == "up" {
		if err := s.apply(ctx); err != nil {
			s.record("error", "automation.reconcile_failed", fmt.Sprintf("WAN recovery failed: %v", err))
			log.Printf("automation WAN recovery failed: %v", err)
			return
		}
		s.record("info", "automation.reconcile_restored", "WAN returned, routes reconciled")
		return
	}

	if snapshot.WAN.State != "up" {
		return
	}

	if openvpnRecoveryNeeded(state, snapshot.OpenVPNRuntime) || subscriptionRecoveryNeeded(state, snapshot.SubscriptionRuntime) {
		if err := s.apply(ctx); err != nil {
			s.record("error", "automation.reconcile_failed", fmt.Sprintf("vpn runtime recovery failed: %v", err))
			log.Printf("automation runtime recovery failed: %v", err)
			return
		}
		s.record("info", "automation.reconcile_restored", "VPN runtimes reconciled automatically")
	}
}

func (s *Supervisor) maybeCleanupTraffic(state config.State) {
	days := state.Automation.TrafficCleanupDays
	if days <= 0 {
		return
	}
	if time.Since(s.lastCleanup) < time.Hour {
		return
	}
	s.lastCleanup = time.Now()

	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	if err := s.status.PurgeTrafficOlderThan(cutoff); err != nil {
		log.Printf("traffic cleanup failed: %v", err)
	} else {
		log.Printf("traffic cleanup: purged data older than %d days", days)
	}
}

func openvpnRecoveryNeeded(state config.State, snapshots []openvpn.RuntimeSnapshot) bool {
	if !hasEnabledOpenVPNRules(state) {
		return false
	}

	for _, snapshot := range snapshots {
		if snapshot.Status == "running" {
			return false
		}
	}

	return true
}

func subscriptionRecoveryNeeded(state config.State, snapshots []subscription.RuntimeSnapshot) bool {
	expected := expectedSubscriptionKeys(state)
	if len(expected) == 0 {
		return false
	}

	actual := make(map[string]string, len(snapshots))
	for _, snapshot := range snapshots {
		actual[snapshot.Key] = snapshot.Status
	}

	for key := range expected {
		if actual[key] != "running" {
			return true
		}
	}
	return false
}

func expectedSubscriptionKeys(state config.State) map[string]struct{} {
	providersByID := make(map[string]config.Provider, len(state.Providers))
	for _, provider := range state.Providers {
		providersByID[provider.ID] = provider
	}

	expected := make(map[string]struct{})
	for _, rule := range state.Rules {
		if !rule.Enabled {
			continue
		}
		provider, exists := providersByID[rule.ProviderID]
		if !exists || !provider.Enabled || provider.Type != config.ProviderTypeSubscription {
			continue
		}
		location := strings.TrimSpace(rule.SelectedLocation)
		if location == "" {
			continue
		}
		expected[provider.ID+"::"+strings.ToLower(location)] = struct{}{}
	}
	return expected
}

func hasEnabledOpenVPNRules(state config.State) bool {
	providersByID := make(map[string]config.Provider, len(state.Providers))
	for _, provider := range state.Providers {
		providersByID[provider.ID] = provider
	}

	for _, rule := range state.Rules {
		if !rule.Enabled {
			continue
		}
		provider, exists := providersByID[rule.ProviderID]
		if exists && provider.Enabled && provider.Type == config.ProviderTypeOpenVPN {
			return true
		}
	}
	return false
}

func hasEnabledRules(state config.State) bool {
	providersByID := make(map[string]config.Provider, len(state.Providers))
	for _, provider := range state.Providers {
		providersByID[provider.ID] = provider
	}

	for _, rule := range state.Rules {
		if !rule.Enabled {
			continue
		}
		provider, exists := providersByID[rule.ProviderID]
		if exists && provider.Enabled {
			return true
		}
	}
	return false
}

func (s *Supervisor) record(level string, kind string, message string) {
	if s.recordEvent == nil {
		return
	}
	s.recordEvent(level, kind, message)
}
