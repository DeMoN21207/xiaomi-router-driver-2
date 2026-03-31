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
	history                     *trafficHistoryStore
	domainTraffic               *domainTrafficStore
	siteTraffic                 *siteTrafficStore
	trafficSampleInterval       time.Duration
	domainTrafficSampleInterval time.Duration
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
		history:                     newTrafficHistoryStore(db, legacyTrafficPath, trafficHistoryRetention),
		domainTraffic:               newDomainTrafficStore(db),
		siteTraffic:                 newSiteTrafficStore(db),
		trafficSampleInterval:       resolveTrafficSampleInterval(),
		domainTrafficSampleInterval: resolveDomainTrafficSampleInterval(),
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
	wan := s.probeWAN(ctx)
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
	expectedSubscriptionKeys := expectedSubscriptionKeysByProvider(state)

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
		args = []string{"-n", "1", "-w", "1500", s.wanProbe}
	} else {
		args = []string{"-c", "1", "-W", "2", s.wanProbe}
	}

	cmd := exec.CommandContext(ctx, pingBinary, args...)
	output, err := cmd.CombinedOutput()
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

func expectedSubscriptionKeysByProvider(state config.State) map[string][]string {
	providersByID := make(map[string]config.Provider, len(state.Providers))
	for _, provider := range state.Providers {
		providersByID[provider.ID] = provider
	}

	byProvider := make(map[string][]string)
	seen := make(map[string]map[string]struct{})
	for _, rule := range state.Rules {
		if !rule.Enabled {
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

		key := provider.ID + "::" + strings.ToLower(location)
		if seen[provider.ID] == nil {
			seen[provider.ID] = make(map[string]struct{})
		}
		if _, exists := seen[provider.ID][key]; exists {
			continue
		}
		seen[provider.ID][key] = struct{}{}
		byProvider[provider.ID] = append(byProvider[provider.ID], key)
	}

	return byProvider
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
