package subscription

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/routing"
	"xiomi-router-driver/internal/runtimehealth"
)

type previousDomainsKey struct{}

// WithPreviousDomains supplies the applied global list before the API replaces
// it. It is only a migration fallback for one legacy runtime without a sidecar.
func WithPreviousDomains(ctx context.Context, entries []string) context.Context {
	return context.WithValue(ctx, previousDomainsKey{}, append([]string(nil), entries...))
}

type applyRollback struct {
	plans []applyPlan
}

func runtimeDomainListPath(configPath string) string {
	return strings.TrimSuffix(configPath, ".json") + ".domains.list"
}

func (r applyRollback) close() {
	for _, plan := range r.plans {
		removeIfExists(plan.ipsetRestorePath)
	}
}

func (m *Manager) prepareRollback(ctx context.Context, instances []*managedInstance) (result applyRollback, resultErr error) {
	defer func() {
		if resultErr != nil {
			result.close()
		}
	}()
	count := 0
	for _, instance := range instances {
		if instance != nil {
			count++
		}
	}
	for _, instance := range instances {
		if instance == nil {
			continue
		}
		configData, err := os.ReadFile(instance.ConfigPath)
		entries, entriesErr := rollbackDomains(ctx, instance, count)
		if err != nil || entriesErr != nil {
			// Incomplete files from an already dead process must not prevent
			// normal recovery. Never discard a running VPN without a rollback plan.
			if !interfaceAlive(instance.InterfaceName) || !runtimehealth.ProcessAlive(instance.PID) {
				continue
			}
			return result, fmt.Errorf("preserve previous VPN %s before apply: %w", instance.Location, errors.Join(err, entriesErr))
		}
		plan := applyPlan{
			desired: desiredInstance{Key: instance.Key, Provider: config.Provider{ID: instance.ProviderID, Name: instance.ProviderName},
				Location: instance.Location, Domains: entries},
			settings: instance.Settings, configPath: instance.ConfigPath, configData: configData,
			domainListPath: runtimeDomainListPath(instance.ConfigPath),
		}
		plan.ipsetRestorePath, err = routing.CaptureIPSet(ctx, instance.Settings.IPSetName, instance.Settings.IPSetName)
		if err != nil {
			m.record("warn", "subscription.rollback_dns_fallback", "Cannot snapshot previous destinations; rollback will rebuild them from saved entries")
		}
		result.plans = append(result.plans, plan)
	}
	return result, nil
}

func rollbackDomains(ctx context.Context, instance *managedInstance, instanceCount int) ([]string, error) {
	data, err := os.ReadFile(runtimeDomainListPath(instance.ConfigPath))
	if err == nil {
		entries := strings.Fields(string(data))
		if len(entries) != instance.DomainCount {
			return nil, fmt.Errorf("saved routing entries do not match runtime metadata")
		}
		return entries, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// Legacy versions kept only dnsmasq fragments. These are complete when all
	// entries were domains; static IPs/CIDRs are absent, so require an exact count.
	data, _ = os.ReadFile(instance.Settings.DNSMasqConfigFile)
	entries := make([]string, 0, instance.DomainCount)
	seen := make(map[string]bool)
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Split(strings.TrimSpace(line), "/")
		if len(parts) != 3 || parts[0] != "ipset=" || parts[1] == "" {
			continue
		}
		for _, set := range strings.Split(parts[2], ",") {
			if set == instance.Settings.IPSetName && !seen[parts[1]] {
				entries = append(entries, parts[1])
				seen[parts[1]] = true
			}
		}
	}
	if len(entries) == instance.DomainCount && instance.DomainCount > 0 {
		return entries, nil
	}
	if previous, ok := ctx.Value(previousDomainsKey{}).([]string); ok && instanceCount == 1 && len(previous) == instance.DomainCount {
		// Any domains that the legacy fragment does expose must belong to the
		// previous list; count alone is not sufficient to establish ownership.
		previousSet := make(map[string]bool, len(previous))
		for _, entry := range previous {
			previousSet[entry] = true
		}
		for _, entry := range entries {
			if !previousSet[entry] {
				return nil, fmt.Errorf("previous entries do not match the active dnsmasq fragment")
			}
		}
		return append([]string(nil), previous...), nil
	}
	return nil, fmt.Errorf("previous routing entries are unavailable; active VPN left unchanged")
}

func (m *Manager) rollbackApply(parent context.Context, cause error, saved applyRollback, touched map[string]bool, started map[string]applyPlan) error {
	// A disconnected HTTP client or an expired apply deadline must not cancel
	// recovery. The independent deadline still bounds a failed rollback.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 90*time.Second)
	defer cancel()
	var failures []error
	instances, err := m.loadInstancesLocked()
	if err != nil {
		failures = append(failures, err)
	}
	current := make(map[string]*managedInstance)
	for _, instance := range instances {
		if instance != nil {
			current[instance.Key] = instance
		}
	}
	for key, instance := range m.current {
		if instance != nil {
			current[key] = instance
		}
	}
	plansByKey := make(map[string]applyPlan)
	for _, plan := range saved.plans {
		plansByKey[plan.desired.Key] = plan
	}
	for _, plan := range started {
		plansByKey[plan.desired.Key] = plan
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(ctx, 10*time.Second)
	// Finish ALL teardown before restoring any old table: new and old plans
	// can share table numbers even when their location keys differ.
	for key := range touched {
		if instance := current[key]; instance != nil {
			err = m.stopInstanceLocked(cleanupCtx, instance)
		} else if plan, exists := plansByKey[key]; exists {
			err = m.routing.RunWithOptions(cleanupCtx, "del", routing.RunOptions{Settings: plan.settings})
		} else {
			err = nil
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("rollback cleanup: %w", err))
		}
	}
	cleanupCancel()
	restored := 0
	for _, plan := range saved.plans {
		if !touched[plan.desired.Key] {
			continue
		}
		if err := m.restorePlanLocked(ctx, plan); err != nil {
			failures = append(failures, fmt.Errorf("restore %s: %w", plan.desired.Location, err))
		} else {
			restored++
		}
	}
	if len(failures) > 0 {
		err := errors.Join(append([]error{cause}, failures...)...)
		m.record("error", "subscription.rollback_failed", err.Error())
		return err
	}
	if restored > 0 {
		m.record("warn", "subscription.rollback_restored", fmt.Sprintf("Replacement failed; restored %d previous VPN runtime(s)", restored))
	}
	return cause
}

func (m *Manager) restorePlanLocked(ctx context.Context, plan applyPlan) (restoreErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	instance, err := m.startPlannedInstance(plan)
	if err != nil {
		return err
	}
	m.current[instance.Key] = instance
	defer func() {
		if restoreErr != nil {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			restoreErr = errors.Join(restoreErr, m.stopInstanceLocked(cleanupCtx, instance))
		}
	}()
	if err := m.saveInstanceLocked(instance); err != nil {
		return err
	}
	if err := waitForInstanceInterface(ctx, instance); err != nil {
		return err
	}
	return m.routing.RunWithOptions(ctx, "add", routing.RunOptions{
		Settings: plan.settings, DomainListPath: plan.domainListPath, IPSetRestorePath: plan.ipsetRestorePath,
	})
}
