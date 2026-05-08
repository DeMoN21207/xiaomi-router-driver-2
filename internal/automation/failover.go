package automation

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/status"
	"xiomi-router-driver/internal/subscription"
)

const (
	providerFailoverApplyTimeout = 2 * time.Minute
	providerProbeTimeout         = 2500 * time.Millisecond
	providerFailoverCooldown     = 60 * time.Second
)

type providerProbeResult struct {
	Healthy   bool
	Detail    string
	Location  string
	LatencyMs int64
}

type failoverOverride struct {
	OriginalProviderID string
	OriginalLocation   string
	ActiveProviderID   string
	ActiveLocation     string
	Mode               string
	Reason             string
	Since              time.Time
}

type providerHealthState struct {
	ProviderID            string
	ProviderName          string
	Status                string
	Score                 int
	ConsecutiveFailures   int
	ConsecutiveSuccesses  int
	UnhealthySince        time.Time
	HealthySince          time.Time
	LastChecked           time.Time
	LastError             string
	Detail                string
	LatencyMs             int64
	RuntimeInterfaceNames []string
}

type failoverRuntime struct {
	overrides map[string]failoverOverride
	health    map[string]providerHealthState
	lastApply time.Time
}

func newFailoverRuntime() failoverRuntime {
	return failoverRuntime{
		overrides: make(map[string]failoverOverride),
		health:    make(map[string]providerHealthState),
	}
}

func (s *Supervisor) maybeProviderFailover(ctx context.Context, state config.State, snapshot status.Snapshot) bool {
	if s.applyState == nil || !state.Automation.ProviderFailover || !hasEnabledRules(state) {
		return false
	}
	if snapshot.WAN.State != "up" {
		return false
	}

	s.failoverMu.Lock()
	defer s.failoverMu.Unlock()

	s.failover.ensure()
	s.pruneFailoverOverrides(state)

	if s.tryRestoreFailoverPrimaries(ctx, state) {
		return true
	}

	appliedState := s.failoverAppliedStateLocked(state)
	currentProviderIDs := enabledProviderIDs(appliedState)
	if len(currentProviderIDs) == 0 {
		return false
	}

	now := time.Now()
	threshold := time.Duration(state.Automation.FailoverFailureSeconds) * time.Second
	if threshold <= 0 {
		threshold = 2 * time.Minute
	}

	for _, providerID := range currentProviderIDs {
		provider, ok := findProvider(appliedState.Providers, providerID)
		if !ok || !provider.Enabled {
			continue
		}

		probe := s.currentProviderProbe(ctx, appliedState, snapshot, provider)
		health := s.updateProviderHealth(provider, probe, now)
		if probe.Healthy {
			continue
		}

		if health.ConsecutiveFailures == 1 {
			s.record("warn", "automation.provider_unhealthy", fmt.Sprintf("%s marked unhealthy: %s", provider.Name, firstNonEmpty(probe.Detail, "health probe failed")))
		}
		if health.UnhealthySince.IsZero() || now.Sub(health.UnhealthySince) < threshold {
			continue
		}
		if health.ConsecutiveFailures < failoverFailureStreak() {
			continue
		}
		if !s.failover.lastApply.IsZero() && now.Sub(s.failover.lastApply) < providerFailoverCooldown {
			continue
		}

		if s.switchProvider(ctx, state, appliedState, providerID, firstNonEmpty(probe.Detail, "health probe failed")) {
			return true
		}
	}

	return false
}

func (s *Supervisor) applyStateRespectingFailover(ctx context.Context, state config.State) error {
	priorityState := s.priorityAppliedState(state)
	hasPriorityDecisions := false
	s.priorityMu.RLock()
	hasPriorityDecisions = len(s.priority.decisions) > 0
	s.priorityMu.RUnlock()

	s.failoverMu.RLock()
	appliedState := s.failoverAppliedStateLocked(priorityState)
	hasOverrides := len(s.failover.overrides) > 0
	s.failoverMu.RUnlock()

	if s.applyState != nil && (hasOverrides || hasPriorityDecisions) {
		return s.applyState(ctx, appliedState)
	}
	return s.apply(ctx)
}

func (s *Supervisor) failoverAppliedState(state config.State) config.State {
	s.failoverMu.RLock()
	defer s.failoverMu.RUnlock()
	return s.failoverAppliedStateLocked(state)
}

func (s *Supervisor) failoverAppliedStateLocked(state config.State) config.State {
	s.failover.ensure()
	if len(s.failover.overrides) == 0 {
		return state
	}

	out := state
	out.Rules = append([]config.Rule(nil), state.Rules...)
	providersByID := providersIndex(state.Providers)

	for index := range out.Rules {
		override, ok := s.failover.overrides[out.Rules[index].ID]
		if !ok {
			continue
		}
		if override.Mode == "direct" {
			out.Rules[index].Enabled = false
			continue
		}
		provider, exists := providersByID[override.ActiveProviderID]
		if !exists || !provider.Enabled {
			continue
		}
		out.Rules[index].ProviderID = override.ActiveProviderID
		out.Rules[index].SelectedLocation = override.ActiveLocation
	}

	return out
}

func (f *failoverRuntime) ensure() {
	if f.overrides == nil {
		f.overrides = make(map[string]failoverOverride)
	}
	if f.health == nil {
		f.health = make(map[string]providerHealthState)
	}
}

func (f *failoverRuntime) hasOverrides() bool {
	f.ensure()
	return len(f.overrides) > 0
}

func (s *Supervisor) pruneFailoverOverrides(state config.State) {
	s.failover.ensure()
	if len(s.failover.overrides) == 0 {
		return
	}

	rulesByID := make(map[string]config.Rule, len(state.Rules))
	for _, rule := range state.Rules {
		rulesByID[rule.ID] = rule
	}
	providersByID := providersIndex(state.Providers)

	for ruleID, override := range s.failover.overrides {
		rule, exists := rulesByID[ruleID]
		if !exists || !rule.Enabled || rule.ProviderID != override.OriginalProviderID || strings.TrimSpace(rule.SelectedLocation) != strings.TrimSpace(override.OriginalLocation) {
			delete(s.failover.overrides, ruleID)
			continue
		}
		if override.Mode == "direct" {
			continue
		}
		if provider, exists := providersByID[override.ActiveProviderID]; !exists || !provider.Enabled {
			delete(s.failover.overrides, ruleID)
		}
	}
}

func (s *Supervisor) tryRestoreFailoverPrimaries(ctx context.Context, state config.State) bool {
	if !s.failover.hasOverrides() {
		return false
	}

	now := time.Now()
	threshold := time.Duration(state.Automation.FailoverRestoreSeconds) * time.Second
	if threshold <= 0 {
		threshold = 60 * time.Second
	}
	if !s.failover.lastApply.IsZero() && now.Sub(s.failover.lastApply) < providerFailoverCooldown {
		return false
	}

	groups := s.failoverOriginalGroups(state)
	for providerID, group := range groups {
		provider, ok := findProvider(state.Providers, providerID)
		if !ok || !provider.Enabled {
			continue
		}

		probe := s.probeProvider(ctx, state, provider, group.preferredLocation)
		health := s.updateProviderHealth(provider, probe, now)
		if !probe.Healthy {
			continue
		}
		if health.HealthySince.IsZero() || now.Sub(health.HealthySince) < threshold {
			continue
		}
		if health.ConsecutiveSuccesses < failoverRestoreStreak() {
			continue
		}

		previous := cloneOverrides(s.failover.overrides)
		for _, ruleID := range group.ruleIDs {
			delete(s.failover.overrides, ruleID)
		}

		applyCtx, cancel := context.WithTimeout(ctx, providerFailoverApplyTimeout)
		err := s.applyState(applyCtx, s.failoverAppliedStateLocked(state))
		cancel()
		if err != nil {
			s.failover.overrides = previous
			s.record("error", "automation.provider_restore_failed", fmt.Sprintf("restore %s failed: %v", provider.Name, err))
			return false
		}

		s.failover.lastApply = now
		s.record("info", "automation.provider_restored", fmt.Sprintf("%s is healthy again; routes restored to primary provider", provider.Name))
		return true
	}

	return false
}

type failoverOriginalGroup struct {
	ruleIDs           []string
	preferredLocation string
}

func (s *Supervisor) failoverOriginalGroups(state config.State) map[string]failoverOriginalGroup {
	groups := make(map[string]failoverOriginalGroup)
	rulesByID := make(map[string]config.Rule, len(state.Rules))
	for _, rule := range state.Rules {
		rulesByID[rule.ID] = rule
	}

	for ruleID, override := range s.failover.overrides {
		if _, exists := rulesByID[ruleID]; !exists {
			continue
		}
		group := groups[override.OriginalProviderID]
		group.ruleIDs = append(group.ruleIDs, ruleID)
		if strings.TrimSpace(group.preferredLocation) == "" {
			group.preferredLocation = override.OriginalLocation
		}
		groups[override.OriginalProviderID] = group
	}

	return groups
}

func (s *Supervisor) currentProviderProbe(ctx context.Context, state config.State, snapshot status.Snapshot, provider config.Provider) providerProbeResult {
	runtimeOK, runtimeDetail := runtimeProviderHealthy(state, snapshot, provider)
	if !runtimeOK {
		return providerProbeResult{Healthy: false, Detail: runtimeDetail}
	}

	interfaceProbe := s.probeProviderRuntimeInterfaces(ctx, snapshot, provider)
	if !interfaceProbe.Healthy {
		return providerProbeResult{Healthy: false, Detail: firstNonEmpty(interfaceProbe.Detail, "provider tunnel interface probe failed")}
	}

	// OpenVPN remote endpoints are often UDP-only and do not reliably answer
	// direct ICMP/TCP probes. Once the process and tunnel probe are healthy, treat
	// the provider as available.
	if provider.Type == config.ProviderTypeOpenVPN {
		return providerProbeResult{Healthy: true, Detail: firstNonEmpty(interfaceProbe.Detail, "runtime is healthy"), LatencyMs: interfaceProbe.LatencyMs}
	}

	probe := s.probeProvider(ctx, state, provider, representativeLocation(state, provider.ID))
	if !probe.Healthy {
		return providerProbeResult{Healthy: false, Detail: firstNonEmpty(probe.Detail, "provider endpoint probe failed"), Location: probe.Location, LatencyMs: probe.LatencyMs}
	}
	if probe.LatencyMs == 0 {
		probe.LatencyMs = interfaceProbe.LatencyMs
	}
	return providerProbeResult{Healthy: true, Detail: firstNonEmpty(probe.Detail, "provider endpoint is reachable"), Location: probe.Location, LatencyMs: probe.LatencyMs}
}

func (s *Supervisor) updateProviderHealth(provider config.Provider, probe providerProbeResult, now time.Time) providerHealthState {
	s.failover.ensure()
	previous := s.failover.health[provider.ID]
	next := previous
	next.ProviderID = provider.ID
	next.ProviderName = provider.Name
	next.LastChecked = now
	next.Detail = strings.TrimSpace(probe.Detail)
	next.LatencyMs = probe.LatencyMs
	next.RuntimeInterfaceNames = nil

	if probe.Healthy {
		next.ConsecutiveSuccesses++
		next.ConsecutiveFailures = 0
		next.LastError = ""
		if next.HealthySince.IsZero() {
			next.HealthySince = now
		}
		next.UnhealthySince = time.Time{}
		next.Score = scoreForProbe(probe, true)
		if next.Score < 100 {
			next.Status = "degraded"
		} else {
			next.Status = "healthy"
		}
	} else {
		next.ConsecutiveFailures++
		next.ConsecutiveSuccesses = 0
		next.LastError = firstNonEmpty(probe.Detail, "provider probe failed")
		if next.UnhealthySince.IsZero() {
			next.UnhealthySince = now
		}
		next.HealthySince = time.Time{}
		next.Score = scoreForProbe(probe, false)
		next.Status = "unhealthy"
	}

	s.failover.health[provider.ID] = next
	return next
}

func scoreForProbe(probe providerProbeResult, healthy bool) int {
	if !healthy {
		return 0
	}
	score := 100
	if probe.LatencyMs >= failoverLatencyWarningMs() {
		score = 65
	}
	return score
}

func failoverFailureStreak() int {
	raw := strings.TrimSpace(os.Getenv("VPN_MANAGER_FAILOVER_FAILURE_STREAK"))
	if raw == "" {
		return 3
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 3
	}
	if value > 10 {
		return 10
	}
	return value
}

func failoverRestoreStreak() int {
	raw := strings.TrimSpace(os.Getenv("VPN_MANAGER_FAILOVER_RESTORE_STREAK"))
	if raw == "" {
		return 3
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 3
	}
	if value > 10 {
		return 10
	}
	return value
}

func failoverLatencyWarningMs() int64 {
	raw := strings.TrimSpace(os.Getenv("VPN_MANAGER_FAILOVER_LATENCY_WARNING_MS"))
	if raw == "" {
		return 2000
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 2000
	}
	return int64(value)
}

func runtimeProviderHealthy(state config.State, snapshot status.Snapshot, provider config.Provider) (bool, string) {
	switch provider.Type {
	case config.ProviderTypeOpenVPN:
		for _, item := range snapshot.OpenVPNRuntime {
			if item.ProviderID != provider.ID {
				continue
			}
			if item.Status == "running" {
				return true, "openvpn runtime is running"
			}
			return false, firstNonEmpty(item.StatusDetail, "openvpn runtime is not running")
		}
		return false, "openvpn runtime is missing"

	case config.ProviderTypeSubscription:
		expected := expectedSubscriptionKeysForProvider(state, provider.ID)
		if len(expected) == 0 {
			return false, "subscription provider has no active runtime locations"
		}
		actual := make(map[string]subscription.RuntimeSnapshot, len(snapshot.SubscriptionRuntime))
		for _, item := range snapshot.SubscriptionRuntime {
			actual[item.Key] = item
		}
		for _, key := range expected {
			item, exists := actual[key]
			if !exists {
				return false, "subscription runtime is missing for active location"
			}
			if item.Status != "running" {
				return false, firstNonEmpty(item.StatusDetail, fmt.Sprintf("subscription location %s is not running", item.Location))
			}
		}
		return true, "subscription runtimes are running"
	default:
		return false, "unsupported provider type"
	}
}

func (s *Supervisor) probeProviderRuntimeInterfaces(ctx context.Context, snapshot status.Snapshot, provider config.Provider) providerProbeResult {
	target := failoverProbeTarget()
	if target == "" || runtime.GOOS == "windows" {
		return providerProbeResult{Healthy: true, Detail: "runtime interface probe disabled"}
	}

	interfaces := runtimeInterfacesForProvider(snapshot, provider)
	if len(interfaces) == 0 {
		return providerProbeResult{Healthy: true, Detail: "runtime interface probe skipped"}
	}

	for _, iface := range interfaces {
		probeCtx, cancel := context.WithTimeout(ctx, providerProbeTimeout)
		result := pingHostViaInterfaceForFailover(probeCtx, target, iface)
		cancel()
		if !result.Healthy {
			result.Detail = fmt.Sprintf("%s via %s failed: %s", target, iface, firstNonEmpty(result.Detail, "probe failed"))
			return result
		}
	}

	return providerProbeResult{Healthy: true, Detail: fmt.Sprintf("%s reachable via %d tunnel interface(s)", target, len(interfaces))}
}

func runtimeInterfacesForProvider(snapshot status.Snapshot, provider config.Provider) []string {
	seen := make(map[string]struct{})
	interfaces := make([]string, 0, 2)
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, exists := seen[name]; exists {
			return
		}
		seen[name] = struct{}{}
		interfaces = append(interfaces, name)
	}

	switch provider.Type {
	case config.ProviderTypeOpenVPN:
		for _, item := range snapshot.OpenVPNRuntime {
			if item.ProviderID == provider.ID && item.Status == "running" {
				add(item.InterfaceName)
			}
		}
	case config.ProviderTypeSubscription:
		for _, item := range snapshot.SubscriptionRuntime {
			if item.ProviderID == provider.ID && item.Status == "running" {
				add(item.InterfaceName)
			}
		}
	}
	return interfaces
}

func failoverProbeTarget() string {
	target := strings.TrimSpace(os.Getenv("VPN_MANAGER_FAILOVER_PROBE"))
	if target == "" {
		return "1.1.1.1"
	}
	switch strings.ToLower(target) {
	case "0", "off", "false", "none", "disabled":
		return ""
	default:
		return target
	}
}

func expectedSubscriptionKeysForProvider(state config.State, providerID string) []string {
	providersByID := providersIndex(state.Providers)
	keys := make([]string, 0, 2)
	seen := make(map[string]struct{})
	for _, rule := range state.Rules {
		if !rule.Enabled || rule.ProviderID != providerID || !ruleHasDomainsForFailover(rule) {
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
		key := providerID + "::" + strings.ToLower(location)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

func (s *Supervisor) switchProvider(ctx context.Context, baseState config.State, appliedState config.State, failedProviderID string, reason string) bool {
	failedProvider, ok := findProvider(baseState.Providers, failedProviderID)
	if !ok {
		return false
	}
	affectedRules := rulesUsingAppliedProvider(baseState, appliedState, failedProviderID)
	if len(affectedRules) == 0 {
		return false
	}

	candidate, locations, probe, ok := s.findFailoverCandidate(ctx, baseState, appliedState, failedProviderID, affectedRules)
	if !ok {
		if s.applyAllDownPolicy(ctx, baseState, failedProvider, affectedRules, reason) {
			return true
		}
		s.record("error", "automation.provider_failover_failed", fmt.Sprintf("%s is unhealthy (%s), but no healthy fallback provider was found", failedProvider.Name, reason))
		return false
	}

	previous := cloneOverrides(s.failover.overrides)
	now := time.Now()
	for _, rule := range affectedRules {
		originalProviderID := rule.ProviderID
		originalLocation := rule.SelectedLocation
		if existing, exists := previous[rule.ID]; exists {
			originalProviderID = existing.OriginalProviderID
			originalLocation = existing.OriginalLocation
		}
		s.failover.overrides[rule.ID] = failoverOverride{
			OriginalProviderID: originalProviderID,
			OriginalLocation:   originalLocation,
			ActiveProviderID:   candidate.ID,
			ActiveLocation:     locations[rule.ID],
			Mode:               "provider",
			Reason:             reason,
			Since:              now,
		}
	}

	applyCtx, cancel := context.WithTimeout(ctx, providerFailoverApplyTimeout)
	err := s.applyState(applyCtx, s.failoverAppliedStateLocked(baseState))
	cancel()
	if err != nil {
		s.failover.overrides = previous
		s.record("error", "automation.provider_failover_failed", fmt.Sprintf("switch %s -> %s failed: %v", failedProvider.Name, candidate.Name, err))
		return false
	}

	s.failover.lastApply = now
	s.record("warn", "automation.provider_failed_over", fmt.Sprintf("%s was unhealthy for %ds (%s); routes switched to %s%s", failedProvider.Name, baseState.Automation.FailoverFailureSeconds, reason, candidate.Name, formatProbeSuffix(probe)))
	return true
}

func (s *Supervisor) applyAllDownPolicy(ctx context.Context, baseState config.State, failedProvider config.Provider, affectedRules []config.Rule, reason string) bool {
	s.record("error", "automation.provider_failover_failed", fmt.Sprintf("%s is unhealthy (%s), no healthy fallback provider was found; direct internet release is disabled", failedProvider.Name, reason))
	return true
}

func (s *Supervisor) findFailoverCandidate(ctx context.Context, baseState config.State, appliedState config.State, failedProviderID string, affectedRules []config.Rule) (config.Provider, map[string]string, providerProbeResult, bool) {
	for _, provider := range orderedCandidateProviders(baseState.Providers, failedProviderID) {
		if !provider.Enabled || provider.ID == failedProviderID {
			continue
		}
		if wouldCreateMultipleOpenVPN(appliedState, failedProviderID, provider) {
			continue
		}

		locations := make(map[string]string, len(affectedRules))
		preferredLocation := ""
		candidateOK := true
		for _, rule := range affectedRules {
			location, ok := s.resolveRuleLocationForProvider(provider, rule)
			if !ok {
				candidateOK = false
				break
			}
			locations[rule.ID] = location
			if preferredLocation == "" {
				preferredLocation = location
			}
		}
		if !candidateOK {
			continue
		}

		probe := s.probeProvider(ctx, baseState, provider, preferredLocation)
		if !probe.Healthy {
			continue
		}
		return provider, locations, probe, true
	}

	return config.Provider{}, nil, providerProbeResult{}, false
}

func orderedCandidateProviders(providers []config.Provider, failedProviderID string) []config.Provider {
	if len(providers) == 0 {
		return nil
	}
	start := 0
	for index, provider := range providers {
		if provider.ID == failedProviderID {
			start = index + 1
			break
		}
	}

	out := make([]config.Provider, 0, len(providers))
	for offset := 0; offset < len(providers); offset++ {
		out = append(out, providers[(start+offset)%len(providers)])
	}
	return out
}

func (s *Supervisor) resolveRuleLocationForProvider(provider config.Provider, rule config.Rule) (string, bool) {
	if provider.Type == config.ProviderTypeOpenVPN {
		return "", true
	}
	if provider.Type != config.ProviderTypeSubscription {
		return "", false
	}

	entries, _, err := subscription.FetchEntriesCached(provider.Source, s.subscriptionRuntimeDir())
	if err != nil || len(entries) == 0 {
		return "", false
	}

	preferences := []string{provider.SelectedLocation, rule.SelectedLocation}
	for _, preference := range preferences {
		if entry, ok := findSubscriptionEntry(entries, preference); ok {
			return entry.Name, true
		}
	}

	return entries[0].Name, true
}

func (s *Supervisor) probeProvider(ctx context.Context, state config.State, provider config.Provider, preferredLocation string) providerProbeResult {
	ctx, cancel := context.WithTimeout(ctx, providerProbeTimeout)
	defer cancel()

	switch provider.Type {
	case config.ProviderTypeSubscription:
		entries, _, err := subscription.FetchEntriesCached(provider.Source, s.subscriptionRuntimeDir())
		if err != nil {
			return providerProbeResult{Healthy: false, Detail: err.Error()}
		}
		entry, ok := findSubscriptionEntry(entries, firstNonEmpty(preferredLocation, provider.SelectedLocation))
		if !ok && len(entries) > 0 {
			entry = entries[0]
			ok = true
		}
		if !ok {
			return providerProbeResult{Healthy: false, Detail: "subscription has no endpoints"}
		}
		result := probeAddress(ctx, entry.Address, "tcp")
		result.Location = entry.Name
		return result

	case config.ProviderTypeOpenVPN:
		remotes, err := parseOpenVPNRemotes(provider.Source, s.dataDir)
		if err != nil {
			return providerProbeResult{Healthy: false, Detail: err.Error()}
		}
		if len(remotes) == 0 {
			return providerProbeResult{Healthy: false, Detail: "openvpn profile has no remote endpoints"}
		}
		for _, remote := range remotes {
			result := probeAddress(ctx, remote.address(), remote.network)
			result.Location = remote.host
			if result.Healthy {
				return result
			}
		}
		return providerProbeResult{Healthy: false, Detail: "all openvpn remotes failed probe"}
	default:
		return providerProbeResult{Healthy: false, Detail: "unsupported provider type"}
	}
}

func (s *Supervisor) subscriptionRuntimeDir() string {
	if strings.TrimSpace(s.dataDir) == "" {
		return ""
	}
	return filepath.Join(s.dataDir, ".vpn-manager", "subscriptions")
}

type openVPNRemote struct {
	host    string
	port    string
	network string
}

func (r openVPNRemote) address() string {
	if strings.TrimSpace(r.port) == "" {
		return r.host
	}
	return net.JoinHostPort(r.host, r.port)
}

func parseOpenVPNRemotes(source string, dataDir string) ([]openVPNRemote, error) {
	path := strings.TrimSpace(source)
	if path == "" {
		return nil, fmt.Errorf("openvpn provider source is empty")
	}
	if !filepath.IsAbs(path) && strings.TrimSpace(dataDir) != "" {
		path = filepath.Join(dataDir, path)
	}

	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("open openvpn profile: %w", err)
	}
	defer file.Close()

	remotes := make([]openVPNRemote, 0, 2)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 || parts[0] != "remote" {
			continue
		}
		remote := openVPNRemote{host: parts[1], network: "ping"}
		if len(parts) >= 3 && isPort(parts[2]) {
			remote.port = parts[2]
		}
		if len(parts) >= 4 && strings.Contains(strings.ToLower(parts[3]), "tcp") {
			remote.network = "tcp"
		}
		remotes = append(remotes, remote)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read openvpn profile: %w", err)
	}

	return remotes, nil
}

func probeAddress(ctx context.Context, address string, network string) providerProbeResult {
	host, port := splitHostPortLenient(address)
	if host == "" {
		return providerProbeResult{Healthy: false, Detail: "empty endpoint host"}
	}

	if network == "tcp" && port != "" {
		start := time.Now()
		dialer := net.Dialer{Timeout: providerProbeTimeout}
		conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
		if err != nil {
			return providerProbeResult{Healthy: false, Detail: err.Error()}
		}
		_ = conn.Close()
		return providerProbeResult{Healthy: true, Detail: "tcp endpoint reachable", LatencyMs: maxInt64(1, time.Since(start).Milliseconds())}
	}

	return pingHostForFailover(ctx, host)
}

func pingHostForFailover(ctx context.Context, host string) providerProbeResult {
	pingBinary, err := exec.LookPath("ping")
	if err != nil {
		return providerProbeResult{Healthy: false, Detail: "ping binary not found"}
	}

	args := []string{"-c", "1", "-W", "2", host}
	if runtime.GOOS == "windows" {
		args = []string{"-n", "1", "-w", "2000", host}
	}
	start := time.Now()
	cmd := exec.CommandContext(ctx, pingBinary, args...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return providerProbeResult{Healthy: false, Detail: "probe timeout"}
	}
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return providerProbeResult{Healthy: false, Detail: detail}
	}
	return providerProbeResult{Healthy: true, Detail: "ping endpoint reachable", LatencyMs: maxInt64(1, time.Since(start).Milliseconds())}
}

func pingHostViaInterfaceForFailover(ctx context.Context, host string, iface string) providerProbeResult {
	pingBinary, err := exec.LookPath("ping")
	if err != nil {
		return providerProbeResult{Healthy: false, Detail: "ping binary not found"}
	}

	start := time.Now()
	cmd := exec.CommandContext(ctx, pingBinary, "-I", iface, "-c", "1", "-W", "2", host)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return providerProbeResult{Healthy: false, Detail: "probe timeout"}
	}
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return providerProbeResult{Healthy: false, Detail: detail}
	}
	return providerProbeResult{Healthy: true, Detail: "tunnel interface probe reachable", LatencyMs: maxInt64(1, time.Since(start).Milliseconds())}
}

func splitHostPortLenient(address string) (string, string) {
	address = strings.TrimSpace(address)
	if address == "" {
		return "", ""
	}
	if host, port, err := net.SplitHostPort(address); err == nil {
		return strings.Trim(host, "[]"), port
	}
	if strings.HasPrefix(address, "[") {
		if end := strings.Index(address, "]"); end > 1 {
			host := address[1:end]
			rest := strings.TrimPrefix(address[end+1:], ":")
			return host, rest
		}
	}
	if strings.Count(address, ":") == 1 {
		parts := strings.SplitN(address, ":", 2)
		if isPort(parts[1]) {
			return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		}
	}
	return strings.Trim(address, "[]"), ""
}

func enabledProviderIDs(state config.State) []string {
	providersByID := providersIndex(state.Providers)
	seen := make(map[string]struct{})
	for _, rule := range state.Rules {
		if !rule.Enabled || !ruleHasDomainsForFailover(rule) {
			continue
		}
		provider, exists := providersByID[rule.ProviderID]
		if !exists || !provider.Enabled {
			continue
		}
		seen[rule.ProviderID] = struct{}{}
	}

	ids := make([]string, 0, len(seen))
	for _, provider := range state.Providers {
		if _, exists := seen[provider.ID]; exists {
			ids = append(ids, provider.ID)
		}
	}
	return ids
}

func rulesUsingAppliedProvider(baseState config.State, appliedState config.State, providerID string) []config.Rule {
	appliedByID := make(map[string]config.Rule, len(appliedState.Rules))
	for _, rule := range appliedState.Rules {
		appliedByID[rule.ID] = rule
	}

	rules := make([]config.Rule, 0)
	providersByID := providersIndex(appliedState.Providers)
	for _, baseRule := range baseState.Rules {
		if !baseRule.Enabled || !ruleHasDomainsForFailover(baseRule) {
			continue
		}
		appliedRule, exists := appliedByID[baseRule.ID]
		if !exists || appliedRule.ProviderID != providerID {
			continue
		}
		provider, exists := providersByID[appliedRule.ProviderID]
		if !exists || !provider.Enabled {
			continue
		}
		rules = append(rules, baseRule)
	}
	return rules
}

func representativeLocation(state config.State, providerID string) string {
	for _, rule := range state.Rules {
		if rule.Enabled && rule.ProviderID == providerID && strings.TrimSpace(rule.SelectedLocation) != "" {
			return strings.TrimSpace(rule.SelectedLocation)
		}
	}
	if provider, ok := findProvider(state.Providers, providerID); ok {
		return provider.SelectedLocation
	}
	return ""
}

func wouldCreateMultipleOpenVPN(appliedState config.State, failedProviderID string, candidate config.Provider) bool {
	if candidate.Type != config.ProviderTypeOpenVPN {
		return false
	}
	providersByID := providersIndex(appliedState.Providers)
	seenOpenVPN := make(map[string]struct{})
	for _, rule := range appliedState.Rules {
		if !rule.Enabled || rule.ProviderID == failedProviderID || !ruleHasDomainsForFailover(rule) {
			continue
		}
		provider, exists := providersByID[rule.ProviderID]
		if !exists || !provider.Enabled || provider.Type != config.ProviderTypeOpenVPN {
			continue
		}
		seenOpenVPN[provider.ID] = struct{}{}
	}
	return len(seenOpenVPN) > 0
}

func providersIndex(providers []config.Provider) map[string]config.Provider {
	out := make(map[string]config.Provider, len(providers))
	for _, provider := range providers {
		out[provider.ID] = provider
	}
	return out
}

func cloneOverrides(source map[string]failoverOverride) map[string]failoverOverride {
	out := make(map[string]failoverOverride, len(source))
	for key, value := range source {
		out[key] = value
	}
	return out
}

func findProvider(providers []config.Provider, id string) (config.Provider, bool) {
	for _, provider := range providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return config.Provider{}, false
}

func findSubscriptionEntry(entries []subscription.Entry, location string) (subscription.Entry, bool) {
	location = strings.TrimSpace(location)
	if location == "" {
		return subscription.Entry{}, false
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Name) == location {
			return entry, true
		}
	}
	lower := strings.ToLower(location)
	for _, entry := range entries {
		if strings.ToLower(strings.TrimSpace(entry.Name)) == lower {
			return entry, true
		}
	}
	return subscription.Entry{}, false
}

func ruleHasDomainsForFailover(rule config.Rule) bool {
	for _, domain := range rule.Domains {
		if strings.TrimSpace(domain) != "" {
			return true
		}
	}
	return false
}

func isPort(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	port, err := strconv.Atoi(value)
	return err == nil && port > 0 && port <= 65535
}

func formatProbeSuffix(result providerProbeResult) string {
	parts := []string{}
	if strings.TrimSpace(result.Location) != "" {
		parts = append(parts, "location="+result.Location)
	}
	if result.LatencyMs > 0 {
		parts = append(parts, fmt.Sprintf("latency=%dms", result.LatencyMs))
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func maxInt64(a int64, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

type FailoverStatus struct {
	Enabled         bool                     `json:"enabled"`
	AllDownMode     string                   `json:"allDownMode"`
	LastApplyAt     string                   `json:"lastApplyAt"`
	ActiveOverrides []FailoverOverrideStatus `json:"activeOverrides"`
	Providers       []FailoverProviderStatus `json:"providers"`
}

type FailoverOverrideStatus struct {
	RuleID               string `json:"ruleId"`
	RuleName             string `json:"ruleName"`
	OriginalProviderID   string `json:"originalProviderId"`
	OriginalProviderName string `json:"originalProviderName"`
	OriginalLocation     string `json:"originalLocation"`
	ActiveProviderID     string `json:"activeProviderId,omitempty"`
	ActiveProviderName   string `json:"activeProviderName,omitempty"`
	ActiveLocation       string `json:"activeLocation,omitempty"`
	Mode                 string `json:"mode"`
	Reason               string `json:"reason"`
	Since                string `json:"since"`
}

type FailoverProviderStatus struct {
	ProviderID           string   `json:"providerId"`
	ProviderName         string   `json:"providerName"`
	Status               string   `json:"status"`
	Score                int      `json:"score"`
	ConsecutiveFailures  int      `json:"consecutiveFailures"`
	ConsecutiveSuccesses int      `json:"consecutiveSuccesses"`
	UnhealthySince       string   `json:"unhealthySince,omitempty"`
	HealthySince         string   `json:"healthySince,omitempty"`
	LastChecked          string   `json:"lastChecked,omitempty"`
	LastError            string   `json:"lastError,omitempty"`
	Detail               string   `json:"detail,omitempty"`
	LatencyMs            int64    `json:"latencyMs,omitempty"`
	RuntimeInterfaces    []string `json:"runtimeInterfaces,omitempty"`
}

func (s *Supervisor) FailoverStatus() FailoverStatus {
	var state config.State
	if s.state != nil {
		if loaded, err := s.state.Load(); err == nil {
			state = loaded
		}
	}

	providersByID := providersIndex(state.Providers)
	rulesByID := make(map[string]config.Rule, len(state.Rules))
	for _, rule := range state.Rules {
		rulesByID[rule.ID] = rule
	}

	s.failoverMu.Lock()
	defer s.failoverMu.Unlock()

	s.failover.ensure()
	result := FailoverStatus{
		Enabled:     state.Automation.ProviderFailover,
		AllDownMode: firstNonEmpty(state.Automation.FailoverAllDownMode, "keep"),
		LastApplyAt: timeString(s.failover.lastApply),
		Providers:   make([]FailoverProviderStatus, 0, len(state.Providers)+len(s.failover.health)),
	}

	for _, provider := range state.Providers {
		health, ok := s.failover.health[provider.ID]
		if !ok {
			result.Providers = append(result.Providers, FailoverProviderStatus{
				ProviderID:   provider.ID,
				ProviderName: provider.Name,
				Status:       "unknown",
				Score:        0,
			})
			continue
		}
		result.Providers = append(result.Providers, failoverProviderStatusFromHealth(health))
	}

	for providerID, health := range s.failover.health {
		if _, exists := providersByID[providerID]; exists {
			continue
		}
		result.Providers = append(result.Providers, failoverProviderStatusFromHealth(health))
	}

	result.ActiveOverrides = make([]FailoverOverrideStatus, 0, len(s.failover.overrides))
	for ruleID, override := range s.failover.overrides {
		rule := rulesByID[ruleID]
		originalProvider := providersByID[override.OriginalProviderID]
		activeProvider := providersByID[override.ActiveProviderID]
		mode := firstNonEmpty(override.Mode, "provider")
		item := FailoverOverrideStatus{
			RuleID:               ruleID,
			RuleName:             firstNonEmpty(rule.Name, ruleID),
			OriginalProviderID:   override.OriginalProviderID,
			OriginalProviderName: firstNonEmpty(originalProvider.Name, override.OriginalProviderID),
			OriginalLocation:     override.OriginalLocation,
			ActiveProviderID:     override.ActiveProviderID,
			ActiveProviderName:   activeProvider.Name,
			ActiveLocation:       override.ActiveLocation,
			Mode:                 mode,
			Reason:               override.Reason,
			Since:                timeString(override.Since),
		}
		if mode == "direct" {
			item.ActiveProviderID = ""
			item.ActiveProviderName = "direct"
			item.ActiveLocation = ""
		}
		result.ActiveOverrides = append(result.ActiveOverrides, item)
	}

	return result
}

func failoverProviderStatusFromHealth(health providerHealthState) FailoverProviderStatus {
	return FailoverProviderStatus{
		ProviderID:           health.ProviderID,
		ProviderName:         health.ProviderName,
		Status:               firstNonEmpty(health.Status, "unknown"),
		Score:                health.Score,
		ConsecutiveFailures:  health.ConsecutiveFailures,
		ConsecutiveSuccesses: health.ConsecutiveSuccesses,
		UnhealthySince:       timeString(health.UnhealthySince),
		HealthySince:         timeString(health.HealthySince),
		LastChecked:          timeString(health.LastChecked),
		LastError:            health.LastError,
		Detail:               health.Detail,
		LatencyMs:            health.LatencyMs,
		RuntimeInterfaces:    append([]string(nil), health.RuntimeInterfaceNames...),
	}
}

func timeString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
