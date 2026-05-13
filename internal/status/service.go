package status

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/domains"
	"xiomi-router-driver/internal/openvpn"
	"xiomi-router-driver/internal/runtimebin"
	"xiomi-router-driver/internal/runtimehealth"
	"xiomi-router-driver/internal/subscription"
)

type FileStatus struct {
	UpdateRoutes bool `json:"updateRoutes"`
}

type BinaryStatus struct {
	OpenVPN bool `json:"openvpn"`
	SingBox bool `json:"singbox"`
}

type ProviderRuntime struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	SelectedLocation string `json:"selectedLocation"`
	Source           string `json:"source"`
	Enabled          bool   `json:"enabled"`
	BinaryAvailable  bool   `json:"binaryAvailable"`
	Health           string `json:"health"`
	HealthDetails    string `json:"healthDetails"`
}

type TrafficRoute struct {
	ProviderID    string `json:"providerId"`
	ProviderName  string `json:"providerName"`
	ProviderType  string `json:"providerType"`
	Location      string `json:"location"`
	InterfaceName string `json:"interfaceName"`
	DomainCount   int    `json:"domainCount"`
	Status        string `json:"status"`
	RXBytes       uint64 `json:"rxBytes"`
	TXBytes       uint64 `json:"txBytes"`
	TotalBytes    uint64 `json:"totalBytes"`
}

type WANStatus struct {
	State      string `json:"state"`
	Probe      string `json:"probe"`
	LatencyMs  int64  `json:"latencyMs"`
	CheckedAt  string `json:"checkedAt"`
	LastError  string `json:"lastError"`
	CheckedVia string `json:"checkedVia"`
}

type Snapshot struct {
	ProvidersCount      int                            `json:"providersCount"`
	RulesCount          int                            `json:"rulesCount"`
	EnabledRules        int                            `json:"enabledRules"`
	DomainsCount        int                            `json:"domainsCount"`
	LastAppliedAt       string                         `json:"lastAppliedAt"`
	LastError           string                         `json:"lastError"`
	UpdatedAt           string                         `json:"updatedAt"`
	Files               FileStatus                     `json:"files"`
	Binaries            BinaryStatus                   `json:"binaries"`
	WAN                 WANStatus                      `json:"wan"`
	Providers           []ProviderRuntime              `json:"providers"`
	OpenVPNRuntime      []openvpn.RuntimeSnapshot      `json:"openvpnRuntime"`
	SubscriptionRuntime []subscription.RuntimeSnapshot `json:"subscriptionRuntime"`
	TrafficRoutes       []TrafficRoute                 `json:"trafficRoutes"`
	ProjectDirectory    string                         `json:"projectDirectory"`
	DataDirectory       string                         `json:"dataDirectory"`
	RuntimeOS           string                         `json:"runtimeOS"`
	HostName            string                         `json:"hostName"`
}

type Service struct {
	state                       *config.Manager
	domains                     *domains.Manager
	openvpn                     *openvpn.Manager
	subscriptions               *subscription.Manager
	updateRoutesPath            string
	appDir                      string
	dataDir                     string
	openvpnBinary               string
	singboxBinary               string
	wanProbe                    string
	wanProbeTimeout             time.Duration
	wanCacheTTL                 time.Duration
	wanMu                       sync.Mutex
	wanCache                    WANStatus
	wanCacheAt                  time.Time
	wanProbeInFlight            bool
	history                     *trafficHistoryStore
	domainTraffic               *domainTrafficStore
	domainHealth                *domainHealthStore
	siteTraffic                 *siteTrafficStore
	trafficSampleInterval       time.Duration
	domainTrafficSampleInterval time.Duration
	domainHealthSampleInterval  time.Duration
	siteTrafficSampleInterval   time.Duration
}

func NewService(
	state *config.Manager,
	domains *domains.Manager,
	openvpnManager *openvpn.Manager,
	subscriptions *subscription.Manager,
	updateRoutesPath string,
	appDir string,
	dataDir string,
	db *sql.DB,
	legacyTrafficPath string,
) *Service {
	openvpnBinary := runtimebin.Resolve(os.Getenv("VPN_MANAGER_OPENVPN_BIN"), "openvpn", appDir, dataDir)
	singboxBinary := runtimebin.Resolve(os.Getenv("VPN_MANAGER_SINGBOX_BIN"), "sing-box", appDir, dataDir)

	wanProbe := strings.TrimSpace(os.Getenv("VPN_MANAGER_WAN_PROBE"))
	if wanProbe == "" {
		wanProbe = "1.1.1.1"
	}
	wanProbeTimeout := resolveDurationFromEnv("VPN_MANAGER_WAN_PROBE_TIMEOUT_MS", 2*time.Second)
	wanCacheTTL := resolveDurationFromEnv("VPN_MANAGER_WAN_CACHE_TTL_MS", 15*time.Second)

	return &Service{
		state:                       state,
		domains:                     domains,
		openvpn:                     openvpnManager,
		subscriptions:               subscriptions,
		updateRoutesPath:            updateRoutesPath,
		appDir:                      appDir,
		dataDir:                     dataDir,
		openvpnBinary:               openvpnBinary,
		singboxBinary:               singboxBinary,
		wanProbe:                    wanProbe,
		wanProbeTimeout:             wanProbeTimeout,
		wanCacheTTL:                 wanCacheTTL,
		history:                     newTrafficHistoryStore(db, legacyTrafficPath, trafficHistoryRetention),
		domainTraffic:               newDomainTrafficStore(db),
		domainHealth:                newDomainHealthStore(db),
		siteTraffic:                 newSiteTrafficStore(db),
		trafficSampleInterval:       resolveTrafficSampleInterval(),
		domainTrafficSampleInterval: resolveDomainTrafficSampleInterval(),
		domainHealthSampleInterval:  resolveDomainHealthSampleInterval(),
		siteTrafficSampleInterval:   resolveSiteTrafficSampleInterval(),
	}
}

func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	state, err := s.state.Load()
	if err != nil {
		return Snapshot{}, err
	}

	domainCount, err := s.domains.Count()
	if err != nil {
		return Snapshot{}, err
	}

	providers := make([]ProviderRuntime, 0, len(state.Providers))
	enabledRules, rulesByProvider, domainsByProvider := summarizeEnabledRules(state)

	binaries := BinaryStatus{
		OpenVPN: s.hasBinary(s.openvpnBinary),
		SingBox: s.hasBinary(s.singboxBinary),
	}
	wan := s.cachedWANStatus(ctx)
	openvpnRuntime := []openvpn.RuntimeSnapshot{}
	if s.openvpn != nil {
		openvpnRuntime, err = s.openvpn.Snapshots()
		if err != nil {
			return Snapshot{}, err
		}
	}
	subscriptionRuntime := []subscription.RuntimeSnapshot{}
	if s.subscriptions != nil {
		subscriptionRuntime, err = s.subscriptions.Snapshots()
		if err != nil {
			return Snapshot{}, err
		}
	}
	trafficRoutes := buildTrafficRoutes(state, openvpnRuntime, subscriptionRuntime, domainsByProvider)
	openvpnRuntimeByProvider := indexOpenVPNRuntimeByProvider(openvpnRuntime)
	subscriptionRuntimeByKey := indexSubscriptionRuntimeByKey(subscriptionRuntime)
	expectedSubscriptionKeys := expectedSubscriptionKeysByProvider(state, subscriptionRuntime)

	for _, provider := range state.Providers {
		runtime := ProviderRuntime{
			ID:               provider.ID,
			Name:             provider.Name,
			Type:             string(provider.Type),
			SelectedLocation: provider.SelectedLocation,
			Source:           provider.Source,
			Enabled:          provider.Enabled,
		}

		switch provider.Type {
		case config.ProviderTypeOpenVPN:
			runtime.BinaryAvailable = binaries.OpenVPN
		case config.ProviderTypeSubscription:
			runtime.BinaryAvailable = binaries.SingBox
		}
		runtime.Health, runtime.HealthDetails = providerHealth(provider, runtime.BinaryAvailable, rulesByProvider[provider.ID], openvpnRuntimeByProvider[provider.ID], expectedSubscriptionKeys[provider.ID], subscriptionRuntimeByKey)

		providers = append(providers, runtime)
	}

	hostName, err := os.Hostname()
	if err != nil {
		hostName = ""
	}

	return Snapshot{
		ProvidersCount: len(state.Providers),
		RulesCount:     len(state.Rules),
		EnabledRules:   enabledRules,
		DomainsCount:   domainCount,
		LastAppliedAt:  state.LastAppliedAt,
		LastError:      state.LastError,
		UpdatedAt:      state.UpdatedAt,
		Files: FileStatus{
			UpdateRoutes: fileExists(s.updateRoutesPath),
		},
		Binaries:            binaries,
		WAN:                 wan,
		Providers:           providers,
		OpenVPNRuntime:      openvpnRuntime,
		SubscriptionRuntime: subscriptionRuntime,
		TrafficRoutes:       trafficRoutes,
		ProjectDirectory:    s.appDir,
		DataDirectory:       s.dataDir,
		RuntimeOS:           runtime.GOOS,
		HostName:            strings.TrimSpace(hostName),
	}, nil
}

func providerHealth(provider config.Provider, binaryAvailable bool, rulesCount int, openvpnSnapshot *openvpn.RuntimeSnapshot, subscriptionKeys []string, subscriptionRuntime map[string]subscription.RuntimeSnapshot) (string, string) {
	if !provider.Enabled {
		return "disabled", "provider is disabled"
	}
	if !binaryAvailable {
		return "degraded", "required binary is not available"
	}
	if strings.TrimSpace(provider.Source) == "" {
		return "degraded", "provider source is empty"
	}
	if rulesCount == 0 {
		return "warning", "provider has no active routes yet"
	}

	switch provider.Type {
	case config.ProviderTypeOpenVPN:
		if openvpnSnapshot == nil {
			return "error", "openvpn runtime is not running"
		}
		if openvpnSnapshot.Status != "running" {
			return "error", firstNonEmpty(openvpnSnapshot.StatusDetail, "openvpn runtime is not healthy")
		}
	case config.ProviderTypeSubscription:
		if len(subscriptionKeys) == 0 {
			return "warning", "subscription provider has no active locations yet"
		}
		for _, key := range subscriptionKeys {
			snapshot, exists := subscriptionRuntime[key]
			if !exists {
				return "error", "subscription runtime is missing for an active location"
			}
			if snapshot.Status != "running" {
				return "error", firstNonEmpty(snapshot.StatusDetail, fmt.Sprintf("subscription location %s is not healthy", snapshot.Location))
			}
		}
	}

	return "ready", fmt.Sprintf("%d routes configured", rulesCount)
}

func (s *Service) cachedWANStatus(ctx context.Context) WANStatus {
	if s == nil {
		return WANStatus{State: "unknown", CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	}

	now := time.Now()
	s.wanMu.Lock()
	cached := s.wanCache
	cachedAt := s.wanCacheAt
	cacheValid := !cachedAt.IsZero() && now.Sub(cachedAt) <= s.wanCacheTTL
	if cacheValid {
		s.wanMu.Unlock()
		return cached
	}
	if !s.wanProbeInFlight {
		s.wanProbeInFlight = true
		go s.refreshWANCache()
	}
	if !cachedAt.IsZero() {
		s.wanMu.Unlock()
		return cached
	}
	s.wanMu.Unlock()

	return WANStatus{
		State:     "checking",
		Probe:     s.wanProbe,
		CheckedAt: now.UTC().Format(time.RFC3339),
	}
}

func (s *Service) refreshWANCache() {
	defer func() {
		s.wanMu.Lock()
		s.wanProbeInFlight = false
		s.wanMu.Unlock()
	}()

	timeout := s.wanProbeTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	status := s.probeWAN(ctx)
	s.wanMu.Lock()
	s.wanCache = status
	s.wanCacheAt = time.Now()
	s.wanMu.Unlock()
}

func (s *Service) probeWAN(ctx context.Context) WANStatus {
	status := WANStatus{
		State:     "unknown",
		Probe:     s.wanProbe,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}

	pingBinary, err := exec.LookPath("ping")
	if err != nil {
		status.LastError = "ping binary not found"
		return status
	}

	status.CheckedVia = pingBinary

	args := []string{}
	if runtime.GOOS == "windows" {
		milliseconds := int(s.wanProbeTimeout / time.Millisecond)
		if milliseconds <= 0 {
			milliseconds = 2000
		}
		args = []string{"-n", "1", "-w", strconv.Itoa(milliseconds), s.wanProbe}
	} else {
		seconds := int((s.wanProbeTimeout + time.Second - 1) / time.Second)
		if seconds <= 0 {
			seconds = 2
		}
		args = []string{"-c", "1", "-W", strconv.Itoa(seconds), s.wanProbe}
	}

	cmd := exec.CommandContext(ctx, pingBinary, args...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		status.State = "down"
		status.LastError = "wan probe timeout"
		return status
	}
	if err != nil {
		status.State = "down"
		status.LastError = strings.TrimSpace(string(output))
		if status.LastError == "" {
			status.LastError = err.Error()
		}
		return status
	}

	status.State = "up"
	status.LatencyMs = parsePingLatency(string(output))
	return status
}

func parsePingLatency(output string) int64 {
	candidates := []string{"time=", "time<"}
	for _, candidate := range candidates {
		index := strings.Index(output, candidate)
		if index < 0 {
			continue
		}

		rest := output[index+len(candidate):]
		end := strings.IndexAny(rest, " \n\r\tm")
		if end < 0 {
			end = len(rest)
		}

		value := strings.Trim(rest[:end], "<>=")
		if value == "" {
			continue
		}

		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			continue
		}
		return int64(parsed)
	}

	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)([0-9]+(?:[.,][0-9]+)?)\D+TTL=`),
		regexp.MustCompile(`(?i)Average\s*=\s*([0-9]+(?:[.,][0-9]+)?)`),
	}
	for _, pattern := range patterns {
		matches := pattern.FindStringSubmatch(output)
		if len(matches) < 2 {
			continue
		}

		value := strings.ReplaceAll(matches[1], ",", ".")
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			continue
		}
		return int64(parsed)
	}

	return 0
}

func resolveDurationFromEnv(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	milliseconds, err := strconv.Atoi(raw)
	if err != nil || milliseconds <= 0 {
		return fallback
	}
	return time.Duration(milliseconds) * time.Millisecond
}

func resolveOptionalDurationEnv(name string, fallback time.Duration, min time.Duration) time.Duration {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch raw {
	case "":
		return fallback
	case "0", "off", "false", "disabled":
		return 0
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < min {
		return fallback
	}
	return parsed
}

func (s *Service) hasBinary(binary string) bool {
	if filepath.IsAbs(binary) {
		info, err := os.Stat(binary)
		return err == nil && !info.IsDir()
	}

	_, err := exec.LookPath(binary)
	return err == nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	return err == nil && !info.IsDir()
}

func buildTrafficRoutes(state config.State, openvpnRuntime []openvpn.RuntimeSnapshot, subscriptionRuntime []subscription.RuntimeSnapshot, domainsByProvider map[string]map[string]struct{}) []TrafficRoute {
	routes := make([]TrafficRoute, 0, len(subscriptionRuntime)+len(state.Providers))
	openvpnRuntimeByProvider := indexOpenVPNRuntimeByProvider(openvpnRuntime)

	for _, instance := range subscriptionRuntime {
		rxBytes, txBytes := readInterfaceTraffic(instance.InterfaceName)
		routes = append(routes, TrafficRoute{
			ProviderID:    instance.ProviderID,
			ProviderName:  instance.ProviderName,
			ProviderType:  string(config.ProviderTypeSubscription),
			Location:      instance.Location,
			InterfaceName: instance.InterfaceName,
			DomainCount:   instance.DomainCount,
			Status:        instance.Status,
			RXBytes:       rxBytes,
			TXBytes:       txBytes,
			TotalBytes:    rxBytes + txBytes,
		})
	}

	for _, provider := range state.Providers {
		if provider.Type != config.ProviderTypeOpenVPN || !provider.Enabled {
			continue
		}
		if len(domainsByProvider[provider.ID]) == 0 {
			continue
		}

		rxBytes, txBytes := readInterfaceTraffic(state.Routing.VPNIface)
		status := interfaceStatus(state.Routing.VPNIface)
		if snapshot := openvpnRuntimeByProvider[provider.ID]; snapshot != nil {
			status = snapshot.Status
		}
		routes = append(routes, TrafficRoute{
			ProviderID:    provider.ID,
			ProviderName:  provider.Name,
			ProviderType:  string(provider.Type),
			Location:      firstNonEmpty(strings.TrimSpace(provider.SelectedLocation), state.Routing.VPNIface),
			InterfaceName: state.Routing.VPNIface,
			DomainCount:   len(domainsByProvider[provider.ID]),
			Status:        status,
			RXBytes:       rxBytes,
			TXBytes:       txBytes,
			TotalBytes:    rxBytes + txBytes,
		})
	}

	sort.Slice(routes, func(i, j int) bool {
		if routes[i].TotalBytes == routes[j].TotalBytes {
			if routes[i].ProviderName == routes[j].ProviderName {
				return routes[i].Location < routes[j].Location
			}
			return routes[i].ProviderName < routes[j].ProviderName
		}
		return routes[i].TotalBytes > routes[j].TotalBytes
	})

	return routes
}

func indexOpenVPNRuntimeByProvider(snapshots []openvpn.RuntimeSnapshot) map[string]*openvpn.RuntimeSnapshot {
	index := make(map[string]*openvpn.RuntimeSnapshot, len(snapshots))
	for i := range snapshots {
		index[snapshots[i].ProviderID] = &snapshots[i]
	}
	return index
}

func indexSubscriptionRuntimeByKey(snapshots []subscription.RuntimeSnapshot) map[string]subscription.RuntimeSnapshot {
	index := make(map[string]subscription.RuntimeSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		index[snapshot.Key] = snapshot
	}
	return index
}

func expectedSubscriptionKeysByProvider(state config.State, runtimeSnapshots []subscription.RuntimeSnapshot) map[string][]string {
	providersByID := make(map[string]config.Provider, len(state.Providers))
	for _, provider := range state.Providers {
		providersByID[provider.ID] = provider
	}

	byProvider := make(map[string][]string)
	seen := make(map[string]map[string]struct{})
	priorityProviders := subscriptionPriorityProviders(state, providersByID)
	for _, rule := range state.Rules {
		if !rule.Enabled {
			continue
		}
		if _, controlled := priorityProviders[rule.ProviderID]; controlled {
			continue
		}

		provider, exists := providersByID[rule.ProviderID]
		if !exists || !provider.Enabled || provider.Type != config.ProviderTypeSubscription {
			continue
		}
		if !ruleHasDomains(rule) {
			continue
		}

		location := strings.TrimSpace(rule.SelectedLocation)
		if location == "" {
			continue
		}

		addSubscriptionKey(byProvider, seen, provider.ID, location)
	}

	for _, policy := range state.PriorityPolicies {
		provider, exists := providersByID[policy.ProviderID]
		if !exists || !priorityPolicyControlsSubscriptionProvider(state, policy, provider) {
			continue
		}
		targets := priorityPolicyTargetSet(policy)
		addedRuntime := false
		for _, snapshot := range runtimeSnapshots {
			if snapshot.ProviderID != provider.ID {
				continue
			}
			if _, ok := targets[strings.ToLower(strings.TrimSpace(snapshot.Location))]; !ok {
				continue
			}
			addSubscriptionKey(byProvider, seen, provider.ID, snapshot.Location)
			addedRuntime = true
		}
		if addedRuntime {
			continue
		}
		addSubscriptionKey(byProvider, seen, provider.ID, preferredPriorityPolicyLocation(policy, time.Now()))
	}

	return byProvider
}

func addSubscriptionKey(byProvider map[string][]string, seen map[string]map[string]struct{}, providerID string, location string) {
	providerID = strings.TrimSpace(providerID)
	location = strings.TrimSpace(location)
	if providerID == "" || location == "" {
		return
	}

	key := providerID + "::" + strings.ToLower(location)
	if seen[providerID] == nil {
		seen[providerID] = make(map[string]struct{})
	}
	if _, exists := seen[providerID][key]; exists {
		return
	}
	seen[providerID][key] = struct{}{}
	byProvider[providerID] = append(byProvider[providerID], key)
}

func subscriptionPriorityProviders(state config.State, providersByID map[string]config.Provider) map[string]struct{} {
	controlled := make(map[string]struct{})
	for _, policy := range state.PriorityPolicies {
		provider, exists := providersByID[policy.ProviderID]
		if !exists || !priorityPolicyControlsSubscriptionProvider(state, policy, provider) {
			continue
		}
		controlled[policy.ProviderID] = struct{}{}
	}
	return controlled
}

func priorityPolicyControlsSubscriptionProvider(state config.State, policy config.PriorityPolicy, provider config.Provider) bool {
	if !policy.Enabled || len(policy.Targets) == 0 {
		return false
	}
	if !provider.Enabled || provider.Type != config.ProviderTypeSubscription {
		return false
	}
	return providerHasRoutedDomains(state, provider.ID)
}

func providerHasRoutedDomains(state config.State, providerID string) bool {
	for _, rule := range state.Rules {
		if rule.Enabled && rule.ProviderID == providerID && ruleHasDomains(rule) {
			return true
		}
	}
	return false
}

func priorityPolicyTargetSet(policy config.PriorityPolicy) map[string]struct{} {
	targets := make(map[string]struct{}, len(policy.Targets))
	for _, target := range policy.Targets {
		location := strings.ToLower(strings.TrimSpace(target.Location))
		if location != "" {
			targets[location] = struct{}{}
		}
	}
	return targets
}

func preferredPriorityPolicyLocation(policy config.PriorityPolicy, now time.Time) string {
	minute := now.Hour()*60 + now.Minute()
	for _, window := range policy.Schedule {
		start, okStart := parsePriorityPolicyClock(window.Start)
		end, okEnd := parsePriorityPolicyClock(window.End)
		if !okStart || !okEnd || start == end {
			continue
		}
		if priorityPolicyWindowContains(start, end, minute) {
			return strings.TrimSpace(window.Location)
		}
	}
	for _, target := range policy.Targets {
		if location := strings.TrimSpace(target.Location); location != "" {
			return location
		}
	}
	return ""
}

func parsePriorityPolicyClock(value string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, false
	}
	hour, okHour := parseClockPart(parts[0], 0, 23)
	minute, okMinute := parseClockPart(parts[1], 0, 59)
	if !okHour || !okMinute {
		return 0, false
	}
	return hour*60 + minute, true
}

func parseClockPart(value string, min int, max int) (int, bool) {
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

func priorityPolicyWindowContains(start int, end int, minute int) bool {
	if start < end {
		return minute >= start && minute < end
	}
	return minute >= start || minute < end
}

func readInterfaceTraffic(interfaceName string) (uint64, uint64) {
	interfaceName = strings.TrimSpace(interfaceName)
	if interfaceName == "" || runtime.GOOS != "linux" {
		return 0, 0
	}

	basePath := filepath.Join("/sys/class/net", interfaceName, "statistics")
	return readUintFile(filepath.Join(basePath, "rx_bytes")), readUintFile(filepath.Join(basePath, "tx_bytes"))
}

func readUintFile(path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	value, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return value
}

func interfaceStatus(name string) string {
	return runtimehealth.Status(name, 0)
}

// PurgeTrafficOlderThan deletes all traffic data (history, domain stats, site/device stats)
// older than the given cutoff time.
func (s *Service) PurgeTrafficOlderThan(cutoff time.Time) error {
	cutoffStr := cutoff.UTC().Format(time.RFC3339)

	if s.history != nil {
		s.history.mu.Lock()
		if err := s.history.ensureReadyLocked(); err == nil {
			_, _ = s.history.db.Exec(`DELETE FROM traffic_history_samples WHERE collected_at < ?`, cutoffStr)
		}
		s.history.mu.Unlock()
	}

	if s.domainTraffic != nil {
		if err := s.domainTraffic.ensureReady(); err == nil {
			s.domainTraffic.mu.Lock()
			_, _ = s.domainTraffic.db.Exec(`DELETE FROM domain_traffic WHERE updated_at < ?`, cutoffStr)
			s.domainTraffic.mu.Unlock()
		}
	}

	if s.siteTraffic != nil {
		if err := s.siteTraffic.ensureReady(); err == nil {
			s.siteTraffic.mu.Lock()
			_, _ = s.siteTraffic.db.Exec(`DELETE FROM site_traffic WHERE updated_at < ?`, cutoffStr)
			_, _ = s.siteTraffic.db.Exec(`DELETE FROM site_traffic_connections WHERE last_seen < ?`, cutoffStr)
			_, _ = s.siteTraffic.db.Exec(`DELETE FROM site_dns_observations WHERE observed_at < ?`, cutoffStr)
			_, _ = s.siteTraffic.db.Exec(`DELETE FROM device_traffic WHERE updated_at < ?`, cutoffStr)
			_, _ = s.siteTraffic.db.Exec(`DELETE FROM device_site_traffic WHERE updated_at < ?`, cutoffStr)
			_, _ = s.siteTraffic.db.Exec(`DELETE FROM device_traffic_history WHERE bucket_at < ?`, cutoffStr)
			_, _ = s.siteTraffic.db.Exec(`DELETE FROM device_site_traffic_history WHERE bucket_at < ?`, cutoffStr)
			s.siteTraffic.mu.Unlock()
		}
	}

	return nil
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

func ruleHasDomains(rule config.Rule) bool {
	for _, domain := range rule.Domains {
		if strings.TrimSpace(domain) != "" {
			return true
		}
	}
	return false
}
