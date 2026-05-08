package automation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/domains"
	"xiomi-router-driver/internal/status"
)

const priorityApplyTimeout = 2 * time.Minute

type priorityOverride struct {
	Location string
	Since    time.Time
}

type priorityDecision struct {
	PolicyID          string
	PolicyName        string
	ProviderID        string
	ProviderName      string
	ActiveLocation    string
	PreferredLocation string
	Mode              string
	Reason            string
	Since             time.Time
	Fingerprint       string
}

type priorityRuntime struct {
	decisions map[string]priorityDecision
	overrides map[string]priorityOverride
	health    map[string]providerHealthState
	lastApply time.Time
}

func newPriorityRuntime() priorityRuntime {
	return priorityRuntime{
		decisions: make(map[string]priorityDecision),
		overrides: make(map[string]priorityOverride),
		health:    make(map[string]providerHealthState),
	}
}

func (p *priorityRuntime) ensure() {
	if p.decisions == nil {
		p.decisions = make(map[string]priorityDecision)
	}
	if p.overrides == nil {
		p.overrides = make(map[string]priorityOverride)
	}
	if p.health == nil {
		p.health = make(map[string]providerHealthState)
	}
}

type PriorityStatus struct {
	LastApplyAt string                 `json:"lastApplyAt"`
	Policies    []PriorityPolicyStatus `json:"policies"`
}

type PriorityPolicyStatus struct {
	PolicyID          string                 `json:"policyId"`
	PolicyName        string                 `json:"policyName"`
	ProviderID        string                 `json:"providerId"`
	ProviderName      string                 `json:"providerName"`
	Enabled           bool                   `json:"enabled"`
	ActiveLocation    string                 `json:"activeLocation,omitempty"`
	PreferredLocation string                 `json:"preferredLocation,omitempty"`
	OverrideLocation  string                 `json:"overrideLocation,omitempty"`
	Mode              string                 `json:"mode,omitempty"`
	Reason            string                 `json:"reason,omitempty"`
	Since             string                 `json:"since,omitempty"`
	Targets           []PriorityTargetStatus `json:"targets"`
}

type PriorityTargetStatus struct {
	Location             string `json:"location"`
	Status               string `json:"status"`
	Score                int    `json:"score"`
	ConsecutiveFailures  int    `json:"consecutiveFailures"`
	ConsecutiveSuccesses int    `json:"consecutiveSuccesses"`
	UnhealthySince       string `json:"unhealthySince,omitempty"`
	HealthySince         string `json:"healthySince,omitempty"`
	LastChecked          string `json:"lastChecked,omitempty"`
	LastError            string `json:"lastError,omitempty"`
	Detail               string `json:"detail,omitempty"`
	LatencyMs            int64  `json:"latencyMs,omitempty"`
	Active               bool   `json:"active"`
	Preferred            bool   `json:"preferred"`
}

func (s *Supervisor) SetPriorityOverride(policyID string, location string) error {
	policyID = strings.TrimSpace(policyID)
	location = strings.TrimSpace(location)
	if policyID == "" {
		return fmt.Errorf("policy id is required")
	}
	if location == "" {
		return fmt.Errorf("location is required")
	}

	s.priorityMu.Lock()
	defer s.priorityMu.Unlock()
	s.priority.ensure()
	s.priority.overrides[policyID] = priorityOverride{Location: location, Since: time.Now()}
	return nil
}

func (s *Supervisor) ClearPriorityOverride(policyID string) {
	policyID = strings.TrimSpace(policyID)
	if policyID == "" {
		return
	}

	s.priorityMu.Lock()
	defer s.priorityMu.Unlock()
	s.priority.ensure()
	delete(s.priority.overrides, policyID)
}

func (s *Supervisor) ApplyPriorityPolicies(ctx context.Context) error {
	if s.state == nil || s.status == nil {
		return nil
	}
	state, err := s.state.Load()
	if err != nil {
		return err
	}
	snapshot, err := s.status.Snapshot(ctx)
	if err != nil {
		return err
	}
	s.maybePriorityPolicies(ctx, state, snapshot)
	return nil
}

func (s *Supervisor) maybePriorityPolicies(ctx context.Context, state config.State, snapshot status.Snapshot) bool {
	if s.applyState == nil {
		return false
	}
	if snapshot.WAN.State != "up" {
		return false
	}

	now := time.Now()
	s.priorityMu.Lock()
	defer s.priorityMu.Unlock()
	s.priority.ensure()
	s.prunePriorityRuntimeLocked(state, now)

	next := make(map[string]priorityDecision)
	for _, policy := range state.PriorityPolicies {
		decision, ok := s.evaluatePriorityPolicyLocked(ctx, state, snapshot, policy, now)
		if ok {
			next[decision.PolicyID] = decision
		}
	}

	needsApply := !samePriorityDecisionSet(s.priority.decisions, next)
	if !needsApply && !priorityRuntimeMatchesDecisions(snapshot, next) {
		needsApply = true
	}
	if !needsApply {
		return false
	}

	appliedState := buildPriorityAppliedStateFromDecisions(state, next)
	applyCtx, cancel := context.WithTimeout(ctx, priorityApplyTimeout)
	err := s.applyState(applyCtx, appliedState)
	cancel()
	if err != nil {
		s.record("error", "automation.priority_apply_failed", fmt.Sprintf("apply priority policies failed: %v", err))
		return false
	}

	s.priority.decisions = next
	s.priority.lastApply = now
	s.record("info", "automation.priority_applied", formatPriorityApplyMessage(next))
	return true
}

func (s *Supervisor) priorityAppliedState(state config.State) config.State {
	s.priorityMu.RLock()
	defer s.priorityMu.RUnlock()
	return buildPriorityAppliedStateFromDecisions(state, s.priority.decisions)
}

func ApplyPriorityDefaults(state config.State, now time.Time) config.State {
	decisions := make(map[string]priorityDecision)
	providers := providersIndex(state.Providers)
	for _, policy := range state.PriorityPolicies {
		if !policy.Enabled || len(policy.Targets) == 0 {
			continue
		}
		provider, ok := providers[policy.ProviderID]
		if !ok || !provider.Enabled || provider.Type != config.ProviderTypeSubscription {
			continue
		}
		location := preferredPriorityLocation(policy, now)
		if location == "" {
			continue
		}
		decisions[policy.ID] = priorityDecision{
			PolicyID:          policy.ID,
			PolicyName:        policy.Name,
			ProviderID:        policy.ProviderID,
			ProviderName:      provider.Name,
			ActiveLocation:    location,
			PreferredLocation: location,
			Mode:              "provider",
			Reason:            "default priority selection",
			Since:             now,
			Fingerprint:       priorityPolicyFingerprint(policy, state),
		}
	}
	return buildPriorityAppliedStateFromDecisions(state, decisions)
}

func buildPriorityAppliedStateFromDecisions(state config.State, decisions map[string]priorityDecision) config.State {
	out := state
	out.PriorityPolicies = nil
	if len(state.PriorityPolicies) == 0 || len(decisions) == 0 {
		out.Rules = append([]config.Rule(nil), state.Rules...)
		return out
	}

	controlledProviders := make(map[string]struct{}, len(decisions))
	for _, decision := range decisions {
		providerID := strings.TrimSpace(decision.ProviderID)
		if providerID != "" {
			controlledProviders[providerID] = struct{}{}
		}
	}

	out.Rules = make([]config.Rule, 0, len(state.Rules)+len(decisions))
	for _, rule := range state.Rules {
		if _, controlled := controlledProviders[rule.ProviderID]; controlled {
			continue
		}
		out.Rules = append(out.Rules, rule)
	}

	appliedProviders := make(map[string]struct{}, len(decisions))
	for _, policy := range state.PriorityPolicies {
		decision, ok := decisions[policy.ID]
		if !ok || decision.Mode == "direct" || strings.TrimSpace(decision.ActiveLocation) == "" {
			continue
		}
		if _, alreadyApplied := appliedProviders[policy.ProviderID]; alreadyApplied {
			continue
		}
		entries := priorityProviderEntries(state, policy.ProviderID)
		if len(entries) == 0 {
			continue
		}
		appliedProviders[policy.ProviderID] = struct{}{}
		out.Rules = append(out.Rules, config.Rule{
			ID:               priorityRuleID(policy.ID),
			Name:             policy.Name,
			ProviderID:       policy.ProviderID,
			SelectedLocation: decision.ActiveLocation,
			Domains:          entries,
			Enabled:          policy.Enabled,
		})
	}
	return out
}

func priorityProviderEntries(state config.State, providerID string) []string {
	raw := make([]string, 0, 16)
	for _, rule := range state.Rules {
		if !rule.Enabled || rule.ProviderID != providerID {
			continue
		}
		raw = append(raw, rule.Domains...)
	}
	return domains.NormalizeEntries(raw)
}

func priorityRuleID(policyID string) string {
	return "__priority_policy_" + strings.TrimSpace(policyID)
}

func (s *Supervisor) prunePriorityRuntimeLocked(state config.State, now time.Time) {
	policies := make(map[string]config.PriorityPolicy, len(state.PriorityPolicies))
	for _, policy := range state.PriorityPolicies {
		policies[policy.ID] = policy
	}
	for policyID, override := range s.priority.overrides {
		policy, exists := policies[policyID]
		if !exists || !locationInPriorityTargets(policy, override.Location) {
			delete(s.priority.overrides, policyID)
			continue
		}
		if boundary := nextPriorityScheduleBoundary(policy, override.Since); !boundary.IsZero() && !now.Before(boundary) {
			delete(s.priority.overrides, policyID)
		}
	}
}

func (s *Supervisor) evaluatePriorityPolicyLocked(ctx context.Context, state config.State, snapshot status.Snapshot, policy config.PriorityPolicy, now time.Time) (priorityDecision, bool) {
	if !policy.Enabled || len(policy.Targets) == 0 {
		return priorityDecision{}, false
	}
	provider, ok := findProvider(state.Providers, policy.ProviderID)
	if !ok || !provider.Enabled || provider.Type != config.ProviderTypeSubscription {
		return priorityDecision{}, false
	}

	preferred := preferredPriorityLocation(policy, now)
	override, hasOverride := s.priority.overrides[policy.ID]
	if hasOverride {
		preferred = override.Location
	}
	if preferred == "" {
		return priorityDecision{}, false
	}

	previous := s.priority.decisions[policy.ID]
	fingerprint := priorityPolicyFingerprint(policy, state)
	candidates := priorityCandidateLocations(policy, preferred)
	selected := ""
	selectedProbe := providerProbeResult{}
	reason := ""
	for _, location := range candidates {
		probe := s.priorityTargetProbe(ctx, state, snapshot, provider, location, previous)
		health := s.updatePriorityTargetHealth(policy, provider, location, probe, now)
		if !probe.Healthy {
			continue
		}
		if location == preferred && previous.ActiveLocation != "" && previous.ActiveLocation != preferred {
			threshold := time.Duration(state.Automation.FailoverRestoreSeconds) * time.Second
			if threshold <= 0 {
				threshold = 60 * time.Second
			}
			if health.HealthySince.IsZero() || now.Sub(health.HealthySince) < threshold || health.ConsecutiveSuccesses < failoverRestoreStreak() {
				continue
			}
		}
		selected = location
		selectedProbe = probe
		if location == preferred {
			reason = "preferred target is healthy"
		} else {
			reason = fmt.Sprintf("preferred target %s is unavailable; using fallback", preferred)
		}
		break
	}

	if selected == "" {
		selected = firstNonEmpty(previous.ActiveLocation, preferred)
		reason = "all priority targets are unhealthy; keeping VPN route because direct internet release is disabled"
	}

	if hasOverride && selected != override.Location {
		delete(s.priority.overrides, policy.ID)
	}

	return priorityDecision{
		PolicyID:          policy.ID,
		PolicyName:        policy.Name,
		ProviderID:        policy.ProviderID,
		ProviderName:      provider.Name,
		ActiveLocation:    selected,
		PreferredLocation: preferred,
		Mode:              "provider",
		Reason:            priorityReasonWithProbe(reason, selectedProbe),
		Since:             decisionSince(previous, now, selected, "provider", fingerprint),
		Fingerprint:       fingerprint,
	}, true
}

func (s *Supervisor) priorityTargetProbe(ctx context.Context, state config.State, snapshot status.Snapshot, provider config.Provider, location string, previous priorityDecision) providerProbeResult {
	if previous.ActiveLocation == location {
		key := provider.ID + "::" + strings.ToLower(strings.TrimSpace(location))
		foundRuntime := false
		for _, item := range snapshot.SubscriptionRuntime {
			if item.Key != key {
				continue
			}
			foundRuntime = true
			if item.Status != "running" {
				return providerProbeResult{Healthy: false, Detail: firstNonEmpty(item.StatusDetail, fmt.Sprintf("subscription location %s is not running", location)), Location: location}
			}
			break
		}
		if !foundRuntime {
			return providerProbeResult{Healthy: false, Detail: "subscription runtime is missing for active priority target", Location: location}
		}
	}

	probe := s.probeProvider(ctx, state, provider, location)
	if probe.Location == "" {
		probe.Location = location
	}
	return probe
}

func (s *Supervisor) updatePriorityTargetHealth(policy config.PriorityPolicy, provider config.Provider, location string, probe providerProbeResult, now time.Time) providerHealthState {
	key := priorityHealthKey(policy.ID, location)
	previous := s.priority.health[key]
	next := previous
	next.ProviderID = provider.ID
	next.ProviderName = provider.Name + " / " + location
	next.LastChecked = now
	next.Detail = strings.TrimSpace(probe.Detail)
	next.LatencyMs = probe.LatencyMs

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
		next.LastError = firstNonEmpty(probe.Detail, "priority target probe failed")
		if next.UnhealthySince.IsZero() {
			next.UnhealthySince = now
		}
		next.HealthySince = time.Time{}
		next.Score = scoreForProbe(probe, false)
		next.Status = "unhealthy"
	}

	s.priority.health[key] = next
	return next
}

func priorityHealthKey(policyID string, location string) string {
	return strings.TrimSpace(policyID) + "::" + strings.ToLower(strings.TrimSpace(location))
}

func preferredPriorityLocation(policy config.PriorityPolicy, now time.Time) string {
	if location := activePriorityScheduleLocation(policy, now); location != "" {
		return location
	}
	if len(policy.Targets) == 0 {
		return ""
	}
	return strings.TrimSpace(policy.Targets[0].Location)
}

func activePriorityScheduleLocation(policy config.PriorityPolicy, now time.Time) string {
	minute := now.Hour()*60 + now.Minute()
	for _, window := range policy.Schedule {
		start, okStart := parsePriorityClock(window.Start)
		end, okEnd := parsePriorityClock(window.End)
		if !okStart || !okEnd || start == end {
			continue
		}
		if priorityWindowContains(start, end, minute) {
			return strings.TrimSpace(window.Location)
		}
	}
	return ""
}

func priorityWindowContains(start int, end int, minute int) bool {
	if start < end {
		return minute >= start && minute < end
	}
	return minute >= start || minute < end
}

func nextPriorityScheduleBoundary(policy config.PriorityPolicy, after time.Time) time.Time {
	if len(policy.Schedule) == 0 || after.IsZero() {
		return time.Time{}
	}
	base := time.Date(after.Year(), after.Month(), after.Day(), 0, 0, 0, 0, after.Location())
	var next time.Time
	for day := 0; day < 3; day++ {
		dayBase := base.AddDate(0, 0, day)
		for _, window := range policy.Schedule {
			for _, raw := range []string{window.Start, window.End} {
				minute, ok := parsePriorityClock(raw)
				if !ok {
					continue
				}
				candidate := dayBase.Add(time.Duration(minute) * time.Minute)
				if !candidate.After(after) {
					continue
				}
				if next.IsZero() || candidate.Before(next) {
					next = candidate
				}
			}
		}
	}
	return next
}

func parsePriorityClock(value string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, false
	}
	hour, okHour := atoiRange(parts[0], 0, 23)
	minute, okMinute := atoiRange(parts[1], 0, 59)
	if !okHour || !okMinute {
		return 0, false
	}
	return hour*60 + minute, true
}

func atoiRange(value string, min int, max int) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	n := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, false
		}
		n = n*10 + int(r-'0')
	}
	return n, n >= min && n <= max
}

func priorityCandidateLocations(policy config.PriorityPolicy, preferred string) []string {
	locations := make([]string, 0, len(policy.Targets))
	for _, target := range policy.Targets {
		location := strings.TrimSpace(target.Location)
		if location != "" {
			locations = append(locations, location)
		}
	}
	if len(locations) == 0 {
		return nil
	}
	start := 0
	for index, location := range locations {
		if strings.EqualFold(location, preferred) {
			start = index
			break
		}
	}
	out := make([]string, 0, len(locations))
	for offset := 0; offset < len(locations); offset++ {
		out = append(out, locations[(start+offset)%len(locations)])
	}
	return out
}

func locationInPriorityTargets(policy config.PriorityPolicy, location string) bool {
	location = strings.TrimSpace(location)
	for _, target := range policy.Targets {
		if strings.EqualFold(strings.TrimSpace(target.Location), location) {
			return true
		}
	}
	return false
}

func priorityPolicyFingerprint(policy config.PriorityPolicy, state config.State) string {
	var b strings.Builder
	b.WriteString(policy.ProviderID)
	b.WriteString("\n")
	for _, entry := range priorityProviderEntries(state, policy.ProviderID) {
		b.WriteString(entry)
		b.WriteString("\n")
	}
	b.WriteString("--targets--\n")
	for _, target := range policy.Targets {
		b.WriteString(target.Location)
		b.WriteString("\n")
	}
	b.WriteString("--schedule--\n")
	for _, window := range policy.Schedule {
		b.WriteString(window.Start)
		b.WriteString("|")
		b.WriteString(window.End)
		b.WriteString("|")
		b.WriteString(window.Location)
		b.WriteString("\n")
	}
	return b.String()
}

func decisionSince(previous priorityDecision, now time.Time, location string, mode string, fingerprint string) time.Time {
	if previous.ActiveLocation == location && previous.Mode == mode && previous.Fingerprint == fingerprint && !previous.Since.IsZero() {
		return previous.Since
	}
	return now
}

func samePriorityDecisionSet(left map[string]priorityDecision, right map[string]priorityDecision) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftDecision := range left {
		rightDecision, ok := right[key]
		if !ok {
			return false
		}
		if leftDecision.ActiveLocation != rightDecision.ActiveLocation ||
			leftDecision.PreferredLocation != rightDecision.PreferredLocation ||
			leftDecision.Mode != rightDecision.Mode ||
			leftDecision.Fingerprint != rightDecision.Fingerprint {
			return false
		}
	}
	return true
}

func priorityRuntimeMatchesDecisions(snapshot status.Snapshot, decisions map[string]priorityDecision) bool {
	if len(decisions) == 0 {
		return true
	}
	actual := make(map[string]string, len(snapshot.SubscriptionRuntime))
	for _, item := range snapshot.SubscriptionRuntime {
		actual[item.Key] = item.Status
	}
	for _, decision := range decisions {
		if decision.Mode == "direct" || decision.ActiveLocation == "" {
			continue
		}
		key := decision.ProviderID + "::" + strings.ToLower(strings.TrimSpace(decision.ActiveLocation))
		if actual[key] != "running" {
			return false
		}
	}
	return true
}

func priorityReasonWithProbe(reason string, probe providerProbeResult) string {
	if suffix := formatProbeSuffix(probe); suffix != "" {
		return reason + suffix
	}
	return reason
}

func formatPriorityApplyMessage(decisions map[string]priorityDecision) string {
	if len(decisions) == 0 {
		return "Priority policies disabled; routes reconciled"
	}
	parts := make([]string, 0, len(decisions))
	for _, decision := range decisions {
		target := decision.ActiveLocation
		if decision.Mode == "direct" {
			target = "direct"
		}
		parts = append(parts, fmt.Sprintf("%s -> %s", decision.PolicyName, target))
	}
	return "Priority policies applied: " + strings.Join(parts, "; ")
}

func (s *Supervisor) PriorityStatus() PriorityStatus {
	var state config.State
	if s.state != nil {
		if loaded, err := s.state.Load(); err == nil {
			state = loaded
		}
	}
	providers := providersIndex(state.Providers)

	s.priorityMu.Lock()
	defer s.priorityMu.Unlock()
	s.priority.ensure()

	result := PriorityStatus{
		LastApplyAt: timeString(s.priority.lastApply),
		Policies:    make([]PriorityPolicyStatus, 0, len(state.PriorityPolicies)),
	}
	for _, policy := range state.PriorityPolicies {
		provider := providers[policy.ProviderID]
		decision := s.priority.decisions[policy.ID]
		override := s.priority.overrides[policy.ID]
		item := PriorityPolicyStatus{
			PolicyID:          policy.ID,
			PolicyName:        policy.Name,
			ProviderID:        policy.ProviderID,
			ProviderName:      provider.Name,
			Enabled:           policy.Enabled,
			ActiveLocation:    decision.ActiveLocation,
			PreferredLocation: decision.PreferredLocation,
			OverrideLocation:  override.Location,
			Mode:              decision.Mode,
			Reason:            decision.Reason,
			Since:             timeString(decision.Since),
			Targets:           make([]PriorityTargetStatus, 0, len(policy.Targets)),
		}
		for _, target := range policy.Targets {
			location := strings.TrimSpace(target.Location)
			health := s.priority.health[priorityHealthKey(policy.ID, location)]
			item.Targets = append(item.Targets, PriorityTargetStatus{
				Location:             location,
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
				Active:               strings.EqualFold(location, decision.ActiveLocation),
				Preferred:            strings.EqualFold(location, decision.PreferredLocation),
			})
		}
		result.Policies = append(result.Policies, item)
	}
	return result
}
