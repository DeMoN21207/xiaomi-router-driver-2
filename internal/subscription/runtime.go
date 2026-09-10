package subscription

import (
	"bufio"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/fileutil"
	"xiomi-router-driver/internal/routing"
	"xiomi-router-driver/internal/runtimebin"
	"xiomi-router-driver/internal/runtimehealth"
)

const interfaceWaitTimeout = 10 * time.Second

var (
	interfaceAlive               = runtimehealth.InterfaceAlive
	waitForInterfacePollInterval = 250 * time.Millisecond
)

type Manager struct {
	db                 *sql.DB
	runtimeDir         string
	legacyManifestPath string
	singBoxBinary      string
	routing            *routing.Runner
	recordEvent        func(level string, kind string, message string)
	mu                 sync.Mutex
	current            map[string]*managedInstance
	initialized        bool
	initErr            error
	snapshotCache      atomic.Pointer[instanceSnapshotCache]
}

type desiredInstance struct {
	Key      string
	Provider config.Provider
	Location string
	Domains  []string
	Entry    Entry
}

type applyPlan struct {
	desired          desiredInstance
	settings         config.RoutingSettings
	domainListPath   string
	configPath       string
	configData       []byte
	ipsetRestorePath string
}

type managedInstance struct {
	Key            string
	ProviderID     string
	ProviderName   string
	Location       string
	InterfaceName  string
	DomainCount    int
	ConfigPath     string
	Settings       config.RoutingSettings
	PID            int
	cmd            *exec.Cmd
	done           <-chan struct{}
	domainListPath string
}

type RuntimeSnapshot struct {
	Key           string `json:"key"`
	ProviderID    string `json:"providerId"`
	ProviderName  string `json:"providerName"`
	Location      string `json:"location"`
	InterfaceName string `json:"interfaceName"`
	TableNum      int    `json:"tableNum"`
	FWMark        string `json:"fwMark"`
	IPSetName     string `json:"ipSetName"`
	DNSMasqConfig string `json:"dnsMasqConfig"`
	DomainCount   int    `json:"domainCount"`
	PID           int    `json:"pid"`
	Status        string `json:"status"`
	StatusDetail  string `json:"statusDetail,omitempty"`
}

type manifest struct {
	Instances []*legacyManagedInstance `json:"instances"`
}

type legacyManagedInstance struct {
	Key            string                 `json:"key"`
	ProviderID     string                 `json:"providerId"`
	ProviderName   string                 `json:"providerName"`
	Location       string                 `json:"location"`
	InterfaceName  string                 `json:"interfaceName"`
	DomainListPath string                 `json:"domainListPath"`
	ConfigPath     string                 `json:"configPath"`
	LogPath        string                 `json:"logPath"`
	Settings       config.RoutingSettings `json:"settings"`
	PID            int                    `json:"pid"`
}

var runSubscriptionRoutingTeardown = func(r *routing.Runner, ctx context.Context, settings config.RoutingSettings) error {
	if r == nil {
		return nil
	}

	return r.RunWithOptions(ctx, "del", routing.RunOptions{
		Settings: settings,
	})
}

func NewManager(appDir string, dataDir string, db *sql.DB, routingRunner *routing.Runner, recordEvent func(level string, kind string, message string)) *Manager {
	singBoxBinary := runtimebin.Resolve(os.Getenv("VPN_MANAGER_SINGBOX_BIN"), "sing-box", appDir, dataDir)

	runtimeDir := filepath.Join(dataDir, ".vpn-manager", "subscriptions")
	return &Manager{
		db:                 db,
		runtimeDir:         runtimeDir,
		legacyManifestPath: filepath.Join(runtimeDir, "runtime.json"),
		singBoxBinary:      singBoxBinary,
		routing:            routingRunner,
		recordEvent:        recordEvent,
		current:            make(map[string]*managedInstance),
	}
}

func (m *Manager) Apply(ctx context.Context, state config.State, enabledRules []config.Rule) (applyErr error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := m.ensureReadyLocked(); err != nil {
		return err
	}
	existingInstances, err := m.loadInstancesLocked()
	if err != nil {
		return err
	}
	m.publishInstances(existingInstances, nil)
	defer m.refreshSnapshotLocked()

	cacheOnly, _ := ctx.Value(cachedEntriesOnlyKey{}).(bool)
	desired, err := m.buildDesired(ctx, state, enabledRules, cacheOnly)
	if err != nil {
		return err
	}
	plans, err := m.buildApplyPlans(desired, state.Routing)
	if err != nil {
		return err
	}
	rollback, err := m.prepareRollback(ctx, existingInstances)
	if err != nil {
		return err
	}
	defer rollback.close()

	copyRouting, _ := ctx.Value(routingSnapshotKey{}).(bool)
	existingCount := 0
	for _, instance := range existingInstances {
		if instance != nil {
			existingCount++
		}
	}
	if copyRouting && existingCount == 1 && len(plans) == 1 && len(rollback.plans) == 1 {
		old := rollback.plans[0]
		// Share the single captured snapshot with fast failover, without another
		// ipset subprocess. Never transfer destinations between different providers.
		if old.desired.Provider.ID == plans[0].desired.Provider.ID && old.ipsetRestorePath != "" {
			path, captureErr := routing.CopyIPSetSnapshot(old.ipsetRestorePath, old.settings.IPSetName, plans[0].settings.IPSetName)
			if captureErr != nil {
				m.record("warn", "subscription.recovery_dns_fallback", "Saved destinations unavailable; rebuilding DNS routing")
			} else if path != "" {
				plans[0].ipsetRestorePath = path
				defer os.Remove(path)
			}
		}
	}

	reusable := make(map[string]*managedInstance, len(plans))
	for _, plan := range plans {
		if existing := m.loadReusableInstanceLocked(plan, existingInstances); existing != nil {
			reusable[plan.desired.Key] = existing
		}
	}
	touched := make(map[string]bool)
	startedPlans := make(map[string]applyPlan)
	defer func() {
		if applyErr != nil && len(touched) > 0 {
			applyErr = m.rollbackApply(ctx, applyErr, rollback, touched, startedPlans)
		}
	}()

	for _, instance := range existingInstances {
		if instance == nil {
			continue
		}
		if _, keep := reusable[instance.Key]; keep {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		touched[instance.Key] = true
		if err := m.stopInstanceLocked(ctx, instance); err != nil {
			return err
		}
	}

	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			return err
		}
		touched[plan.desired.Key] = true
		startedPlans[plan.desired.Key] = plan
		if existing := reusable[plan.desired.Key]; existing != nil {
			if err := m.applyRoutingToInstanceLocked(ctx, existing, plan); err != nil {
				return fmt.Errorf("apply routing for %s: %w", plan.desired.Location, err)
			}
			continue
		}

		instance, err := m.startPlannedInstance(plan)
		if err != nil {
			return err
		}

		m.current[plan.desired.Key] = instance
		if err := m.saveInstanceLocked(instance); err != nil {
			return err
		}

		if err := waitForInstanceInterface(ctx, instance); err != nil {
			return fmt.Errorf("wait for %s interface: %w", plan.desired.Location, err)
		}

		if err := m.routing.RunWithOptions(ctx, "add", routing.RunOptions{
			Settings:         instance.Settings,
			DomainListPath:   plan.domainListPath,
			IPSetRestorePath: plan.ipsetRestorePath,
		}); err != nil {
			return fmt.Errorf("apply routing for %s: %w", plan.desired.Location, err)
		}

		if err := m.saveInstanceLocked(instance); err != nil {
			return err
		}
	}

	return m.pruneRuntimeFilesLocked()
}

func (m *Manager) Cleanup(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureReadyLocked(); err != nil {
		return err
	}
	m.refreshSnapshotLocked()
	defer m.refreshSnapshotLocked()

	return m.cleanupLocked(ctx)
}

func (m *Manager) Snapshots() ([]RuntimeSnapshot, error) {
	// Route application may take seconds. Readers use the last published copy
	// while the writer owns the runtime lock, then independently assess its PIDs.
	if m.mu.TryLock() {
		if err := m.ensureReadyLocked(); err != nil {
			m.publishInstances(nil, err)
		} else {
			m.refreshSnapshotLocked()
		}
		m.mu.Unlock()
	}
	cached := m.snapshotCache.Load()
	if cached == nil {
		return []RuntimeSnapshot{}, nil
	}
	if cached.err != nil {
		return nil, cached.err
	}
	instances := cached.instances
	snapshots := make([]RuntimeSnapshot, 0, len(instances))
	for _, instance := range instances {
		if instance == nil {
			continue
		}
		snapshots = append(snapshots, m.snapshotFromInstance(instance))
	}

	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].ProviderName == snapshots[j].ProviderName {
			return snapshots[i].Location < snapshots[j].Location
		}
		return snapshots[i].ProviderName < snapshots[j].ProviderName
	})

	return snapshots, nil
}

func (m *Manager) startPlannedInstance(plan applyPlan) (*managedInstance, error) {
	if err := writeDomainList(plan.domainListPath, plan.desired.Domains); err != nil {
		return nil, fmt.Errorf("write domain list for %s: %w", plan.desired.Location, err)
	}

	if err := fileutil.WriteFileAtomic(plan.configPath, plan.configData, 0o600); err != nil {
		removeIfExists(plan.domainListPath)
		return nil, fmt.Errorf("write sing-box config for %s: %w", plan.desired.Location, err)
	}

	cmd := exec.Command(m.singBoxBinary, "run", "-c", plan.configPath)
	cmd.Dir = m.runtimeDir

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		removeIfExists(plan.domainListPath)
		removeIfExists(plan.configPath)
		return nil, fmt.Errorf("prepare stdout for %s: %w", plan.desired.Location, err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		removeIfExists(plan.domainListPath)
		removeIfExists(plan.configPath)
		return nil, fmt.Errorf("prepare stderr for %s: %w", plan.desired.Location, err)
	}

	if err := cmd.Start(); err != nil {
		removeIfExists(plan.domainListPath)
		removeIfExists(plan.configPath)
		return nil, fmt.Errorf("start sing-box for %s: %w", plan.desired.Location, err)
	}

	done := make(chan struct{})
	instance := &managedInstance{
		Key:            plan.desired.Key,
		ProviderID:     plan.desired.Provider.ID,
		ProviderName:   plan.desired.Provider.Name,
		Location:       plan.desired.Location,
		InterfaceName:  plan.settings.VPNIface,
		DomainCount:    len(plan.desired.Domains),
		ConfigPath:     plan.configPath,
		Settings:       plan.settings,
		PID:            cmd.Process.Pid,
		cmd:            cmd,
		done:           done,
		domainListPath: plan.domainListPath,
	}

	go m.streamRuntimeLogs(plan.desired.Provider.Name, plan.desired.Location, stdoutPipe)
	go m.streamRuntimeLogs(plan.desired.Provider.Name, plan.desired.Location, stderrPipe)
	go m.watchInstanceWithDone(plan.desired.Key, cmd, done)

	m.record("info", "subscription.runtime_started", fmt.Sprintf("%s: %s started on %s", plan.desired.Provider.Name, plan.desired.Location, plan.settings.VPNIface))
	return instance, nil
}

func (m *Manager) streamRuntimeLogs(providerName string, location string, reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 16*1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		level := runtimeLogLevel(line)
		if !shouldRecordRuntimeLog(line, level) {
			continue
		}
		m.record(level, "subscription.runtime_log", fmt.Sprintf("%s: %s: %s", providerName, location, line))
	}
	if err := scanner.Err(); err != nil {
		m.record("warn", "subscription.runtime_log", fmt.Sprintf("%s: %s: log stream error: %v", providerName, location, err))
	}
}

func (m *Manager) watchInstance(key string, cmd *exec.Cmd) {
	m.watchInstanceWithDone(key, cmd, nil)
}

func (m *Manager) watchInstanceWithDone(key string, cmd *exec.Cmd, done chan struct{}) {
	err := cmd.Wait()
	if done != nil {
		close(done)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	defer m.refreshSnapshotLocked()

	current, exists := m.current[key]
	if !exists {
		return
	}
	if current.PID != 0 && cmd.Process != nil && current.PID != cmd.Process.Pid {
		return
	}

	delete(m.current, key)
	_ = m.deleteInstanceLocked(key)
	removeIfExists(current.domainListPath)
	removeIfExists(current.ConfigPath)
	// Keep the policy route fail-closed until the supervisor replaces the dead
	// runtime. Explicit Apply/Cleanup paths own routing teardown and are serialized.
	_ = m.pruneRuntimeFilesLocked()

	if err != nil {
		m.record("error", "subscription.runtime_failed", fmt.Sprintf("%s: %s stopped unexpectedly on %s: %v", current.ProviderName, current.Location, current.InterfaceName, err))
	}
}

func (m *Manager) cleanupLocked(ctx context.Context) error {
	instances, err := m.loadInstancesLocked()
	if err != nil {
		return err
	}

	handled := make(map[string]struct{}, len(instances))
	var cleanupErrors []error

	for _, instance := range instances {
		if instance == nil {
			continue
		}
		handled[instance.Key] = struct{}{}

		if current := m.current[instance.Key]; current != nil {
			delete(m.current, instance.Key)
			removeIfExists(current.domainListPath)
			if current.cmd != nil && current.cmd.Process != nil {
				_ = current.cmd.Process.Kill()
			}
		} else if instance.PID > 0 {
			_, _ = runtimehealth.KillIfMatches(instance.PID, m.singBoxBinary, instance.ConfigPath)
		}

		if err := m.routing.RunWithOptions(ctx, "del", routing.RunOptions{
			Settings: instance.Settings,
		}); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}

		removeIfExists(instance.ConfigPath)
	}

	for key, current := range m.current {
		if current == nil {
			delete(m.current, key)
			continue
		}
		if _, exists := handled[key]; exists {
			continue
		}

		delete(m.current, key)
		removeIfExists(current.domainListPath)
		if current.cmd != nil && current.cmd.Process != nil {
			_ = current.cmd.Process.Kill()
		}

		if err := m.routing.RunWithOptions(ctx, "del", routing.RunOptions{
			Settings: current.Settings,
		}); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}

		removeIfExists(current.ConfigPath)
	}

	if err := m.clearInstancesLocked(); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if err := m.pruneRuntimeFilesLocked(); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}

	if len(cleanupErrors) > 0 {
		return errors.Join(cleanupErrors...)
	}
	return nil
}

func (m *Manager) buildApplyPlans(desired []desiredInstance, base config.RoutingSettings) ([]applyPlan, error) {
	plans := make([]applyPlan, 0, len(desired))
	for index, item := range desired {
		hash := shortHash(item.Provider.ID + "\n" + item.Location)
		settings := deriveRoutingSettings(base, hash, index)
		configData, err := json.MarshalIndent(buildSingBoxConfig(settings.VPNIface, index, item.Entry.Outbound), "", "  ")
		if err != nil {
			return nil, fmt.Errorf("marshal sing-box config for %s: %w", item.Location, err)
		}
		configData = append(configData, '\n')

		plans = append(plans, applyPlan{
			desired:        item,
			settings:       settings,
			domainListPath: filepath.Join(m.runtimeDir, hash+".domains.list"),
			configPath:     filepath.Join(m.runtimeDir, hash+".json"),
			configData:     configData,
		})
	}
	return plans, nil
}

func (m *Manager) loadReusableInstanceLocked(plan applyPlan, existing []*managedInstance) *managedInstance {
	if current := m.current[plan.desired.Key]; current != nil {
		if m.canReuseInstance(current, plan) {
			return current
		}
		return nil
	}

	for _, instance := range existing {
		if instance == nil || instance.Key != plan.desired.Key {
			continue
		}
		if m.canReuseInstance(instance, plan) {
			return instance
		}
		return nil
	}

	return nil
}

func (m *Manager) canReuseInstance(instance *managedInstance, plan applyPlan) bool {
	if !sameRuntimeConfig(instance, plan) {
		return false
	}
	if !interfaceAlive(instance.InterfaceName) {
		return false
	}
	if instance.PID <= 0 {
		return false
	}
	if instance.done != nil {
		// An owned process cannot have its PID recycled before Wait completes.
		select {
		case <-instance.done:
			return false
		default:
			return true
		}
	}
	return runtimehealth.ProcessAlive(instance.PID, m.singBoxBinary, instance.ConfigPath)
}

func (m *Manager) applyRoutingToInstanceLocked(ctx context.Context, instance *managedInstance, plan applyPlan) error {
	target := instance
	if current := m.current[instance.Key]; current != nil {
		target = current
	}

	if err := writeDomainList(plan.domainListPath, plan.desired.Domains); err != nil {
		return fmt.Errorf("write domain list for %s: %w", plan.desired.Location, err)
	}
	if err := waitForInterfaceContext(ctx, plan.settings.VPNIface, interfaceWaitTimeout); err != nil {
		return fmt.Errorf("wait for %s interface before routing sync: %w", plan.desired.Location, err)
	}

	if err := m.routing.RunWithOptions(ctx, "sync", routing.RunOptions{
		Settings:         plan.settings,
		DomainListPath:   plan.domainListPath,
		IPSetRestorePath: plan.ipsetRestorePath,
	}); err != nil {
		if err := m.routing.RunWithOptions(ctx, "add", routing.RunOptions{
			Settings:         plan.settings,
			DomainListPath:   plan.domainListPath,
			IPSetRestorePath: plan.ipsetRestorePath,
		}); err != nil {
			return err
		}
	}

	target.ProviderID = plan.desired.Provider.ID
	target.ProviderName = plan.desired.Provider.Name
	target.Location = plan.desired.Location
	target.InterfaceName = plan.settings.VPNIface
	target.DomainCount = len(plan.desired.Domains)
	target.ConfigPath = plan.configPath
	target.Settings = plan.settings
	target.domainListPath = plan.domainListPath
	return m.saveInstanceLocked(target)
}

func (m *Manager) stopInstanceLocked(ctx context.Context, instance *managedInstance) error {
	if instance == nil {
		return nil
	}

	var cleanupErrors []error
	if current := m.current[instance.Key]; current != nil {
		delete(m.current, instance.Key)
		removeIfExists(current.domainListPath)
		if current.cmd != nil && current.cmd.Process != nil {
			_ = current.cmd.Process.Kill()
			if current.done != nil {
				select {
				case <-current.done:
				case <-ctx.Done():
					cleanupErrors = append(cleanupErrors, ctx.Err())
				}
			}
		} else if current.PID > 0 {
			_, _ = runtimehealth.KillIfMatches(current.PID, m.singBoxBinary, current.ConfigPath)
		}
	} else if instance.PID > 0 {
		_, _ = runtimehealth.KillIfMatches(instance.PID, m.singBoxBinary, instance.ConfigPath)
	}

	if err := m.routing.RunWithOptions(ctx, "del", routing.RunOptions{
		Settings: instance.Settings,
	}); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	if err := m.deleteInstanceLocked(instance.Key); err != nil {
		cleanupErrors = append(cleanupErrors, err)
	}
	removeIfExists(instance.ConfigPath)
	removeIfExists(instance.domainListPath)

	return errors.Join(cleanupErrors...)
}

func (m *Manager) buildDesired(ctx context.Context, state config.State, enabledRules []config.Rule, cacheOnly bool) ([]desiredInstance, error) {
	providersByID := make(map[string]config.Provider, len(state.Providers))
	for _, provider := range state.Providers {
		providersByID[provider.ID] = provider
	}

	groups := make(map[string]*desiredInstance)
	seenDomains := make(map[string]map[string]struct{})

	for _, rule := range enabledRules {
		provider, exists := providersByID[rule.ProviderID]
		if !exists || !provider.Enabled || provider.Type != config.ProviderTypeSubscription {
			continue
		}

		location := strings.TrimSpace(rule.SelectedLocation)
		if location == "" {
			return nil, fmt.Errorf("subscription rule %q must have a selected location", rule.Name)
		}

		key := provider.ID + "::" + strings.ToLower(location)
		group, exists := groups[key]
		if !exists {
			group = &desiredInstance{
				Key:      key,
				Provider: provider,
				Location: location,
				Domains:  make([]string, 0, len(rule.Domains)),
			}
			groups[key] = group
			seenDomains[key] = make(map[string]struct{}, len(rule.Domains))
		}

		for _, domain := range rule.Domains {
			if _, alreadyAdded := seenDomains[key][domain]; alreadyAdded {
				continue
			}
			seenDomains[key][domain] = struct{}{}
			group.Domains = append(group.Domains, domain)
		}
	}

	if len(groups) == 0 {
		return nil, nil
	}

	activeGroups := make([]*desiredInstance, 0, len(groups))
	for _, group := range groups {
		if group == nil || len(group.Domains) == 0 {
			continue
		}
		activeGroups = append(activeGroups, group)
	}
	if len(activeGroups) == 0 {
		return nil, nil
	}

	entriesByProvider := make(map[string][]Entry, len(activeGroups))
	for _, group := range activeGroups {
		if _, loaded := entriesByProvider[group.Provider.ID]; loaded {
			continue
		}
		var entries []Entry
		var fetchMode entriesFetchMode
		var err error
		if cacheOnly {
			entries, err = LoadCachedEntries(group.Provider.Source, m.runtimeDir)
		} else {
			entries, fetchMode, err = FetchEntriesCachedContext(ctx, group.Provider.Source, m.runtimeDir)
		}
		if err != nil {
			return nil, fmt.Errorf("load subscription %q: %w", group.Provider.Name, err)
		}
		if fetchMode == entriesFetchStaleCacheFallback {
			m.record("warn", "subscription.source_cache_used", fmt.Sprintf("%s source is unavailable, using cached subscription snapshot", group.Provider.Name))
		}
		entriesByProvider[group.Provider.ID] = entries
	}

	desired := make([]desiredInstance, 0, len(activeGroups))
	for _, group := range activeGroups {
		entry, found := findEntry(entriesByProvider[group.Provider.ID], group.Location)
		if !found {
			return nil, fmt.Errorf("location %q not found in provider %q", group.Location, group.Provider.Name)
		}
		if issue := EntryEndpointIssue(entry); issue != "" {
			return nil, fmt.Errorf("location %q in provider %q cannot be used: %s", group.Location, group.Provider.Name, issue)
		}
		group.Entry = entry
		desired = append(desired, *group)
	}

	sort.Slice(desired, func(i, j int) bool {
		if desired[i].Provider.Name == desired[j].Provider.Name {
			return desired[i].Location < desired[j].Location
		}
		return desired[i].Provider.Name < desired[j].Provider.Name
	})

	return desired, nil
}

func buildSingBoxConfig(interfaceName string, index int, outbound map[string]any) map[string]any {
	proxyOutbound := cloneMap(outbound)
	proxyOutbound["tag"] = "proxy"

	return map[string]any{
		"inbounds": []any{
			map[string]any{
				"type":           "tun",
				"tag":            "tun-in",
				"interface_name": interfaceName,
				"address":        []string{tunAddress(index)},
				"mtu":            1400,
				"auto_route":     false,
				"strict_route":   true,
				"stack":          "system",
			},
		},
		"outbounds": []any{proxyOutbound},
		"route": map[string]any{
			"final":                 "proxy",
			"auto_detect_interface": true,
		},
	}
}

func cloneMap(source map[string]any) map[string]any {
	cloned := make(map[string]any, len(source))
	for key, value := range source {
		cloned[key] = cloneValue(value)
	}
	return cloned
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = cloneValue(item)
		}
		return items
	case []string:
		items := make([]string, len(typed))
		copy(items, typed)
		return items
	default:
		return typed
	}
}

func writeDomainList(path string, domains []string) error {
	content := strings.Join(domains, "\n")
	if content != "" {
		content += "\n"
	}
	return fileutil.WriteFileAtomic(path, []byte(content), 0o644)
}

func deriveRoutingSettings(base config.RoutingSettings, hash string, index int) config.RoutingSettings {
	settings := base
	settings.VPNRouteMode = "dev"
	settings.VPNGateway = ""
	settings.VPNIface = trimTo("sb"+hash[:10], 15)
	settings.TableNum = base.TableNum + index
	settings.FWMark = incrementMark(base.FWMark, index)
	settings.IPSetName = trimTo(base.IPSetName+"_"+hash[:8], 31)
	settings.DNSMasqConfigFile = appendFileSuffix(base.DNSMasqConfigFile, "_"+hash[:8])
	return settings
}

func incrementMark(base string, offset int) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "0x1"
	}

	if strings.HasPrefix(strings.ToLower(base), "0x") {
		value, err := strconv.ParseInt(base[2:], 16, 64)
		if err != nil {
			value = 1
		}
		return fmt.Sprintf("0x%x", value+int64(offset))
	}

	value, err := strconv.Atoi(base)
	if err != nil {
		value = 1
	}
	return strconv.Itoa(value + offset)
}

func appendFileSuffix(path string, suffix string) string {
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	if ext == "" {
		return base + suffix
	}
	return base + suffix + ext
}

func shortHash(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func trimTo(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func tunAddress(index int) string {
	thirdOctet := index / 64
	fourthOctet := (index%64)*4 + 1
	return fmt.Sprintf("172.29.%d.%d/30", thirdOctet, fourthOctet)
}

func waitForInterface(name string, timeout time.Duration) error {
	return waitForInterfaceContext(context.Background(), name, timeout)
}

func waitForInterfaceContext(ctx context.Context, name string, timeout time.Duration) error {
	return waitForInterfaceReady(ctx, name, timeout, nil)
}

func waitForInstanceInterface(ctx context.Context, instance *managedInstance) error {
	return waitForInterfaceReady(ctx, instance.Settings.VPNIface, interfaceWaitTimeout, instance.done)
}

func waitForInterfaceReady(ctx context.Context, name string, timeout time.Duration, processDone <-chan struct{}) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(waitForInterfacePollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("interface %s did not appear: %w", name, err)
		}
		select {
		case <-processDone:
			return fmt.Errorf("VPN process exited before interface %s was ready", name)
		default:
		}
		if interfaceAlive(name) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("interface %s did not appear: %w", name, ctx.Err())
		case <-processDone:
			return fmt.Errorf("VPN process exited before interface %s was ready", name)
		case <-ticker.C:
		}
	}
}

func findEntry(entries []Entry, location string) (Entry, bool) {
	for _, entry := range entries {
		if entry.Name == location {
			return entry, true
		}
	}

	lowerLocation := strings.ToLower(location)
	for _, entry := range entries {
		if strings.ToLower(entry.Name) == lowerLocation {
			return entry, true
		}
	}

	return Entry{}, false
}

func (m *Manager) snapshotFromInstance(instance *managedInstance) RuntimeSnapshot {
	assessment := runtimehealth.Assess(runtimehealth.Check{
		InterfaceName:        instance.InterfaceName,
		PID:                  instance.PID,
		ProcessMarkers:       []string{m.singBoxBinary, instance.ConfigPath},
		EnableInterfaceProbe: false,
	})

	return RuntimeSnapshot{
		Key:           instance.Key,
		ProviderID:    instance.ProviderID,
		ProviderName:  instance.ProviderName,
		Location:      instance.Location,
		InterfaceName: instance.InterfaceName,
		TableNum:      instance.Settings.TableNum,
		FWMark:        instance.Settings.FWMark,
		IPSetName:     instance.Settings.IPSetName,
		DNSMasqConfig: instance.Settings.DNSMasqConfigFile,
		DomainCount:   instance.DomainCount,
		PID:           instance.PID,
		Status:        assessment.Status,
		StatusDetail:  assessment.Detail,
	}
}

func countDomainEntries(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}

	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		count++
	}
	return count
}

func runtimeLogLevel(line string) string {
	lower := strings.ToLower(strings.TrimSpace(line))
	switch {
	case strings.Contains(lower, "fatal"), strings.Contains(lower, "panic"), strings.Contains(lower, "error"):
		return "error"
	case strings.Contains(lower, "warn"):
		return "warn"
	default:
		return "info"
	}
}

func shouldRecordRuntimeLog(line string, level string) bool {
	lower := strings.ToLower(strings.TrimSpace(line))
	if strings.Contains(lower, "icmp is not supported by default outbound: proxy") {
		return false
	}

	if level == "info" {
		return false
	}

	return !strings.Contains(lower, "trace") && !strings.Contains(lower, "debug")
}

func sameRuntimeConfig(instance *managedInstance, plan applyPlan) bool {
	if instance == nil {
		return false
	}
	if instance.Key != plan.desired.Key ||
		instance.ProviderID != plan.desired.Provider.ID ||
		strings.TrimSpace(instance.Location) != strings.TrimSpace(plan.desired.Location) ||
		strings.TrimSpace(instance.InterfaceName) != strings.TrimSpace(plan.settings.VPNIface) ||
		filepath.Clean(strings.TrimSpace(instance.ConfigPath)) != filepath.Clean(strings.TrimSpace(plan.configPath)) ||
		instance.Settings != plan.settings {
		return false
	}
	return fileContentsEqual(plan.configPath, plan.configData)
}

func fileContentsEqual(path string, expected []byte) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return string(data) == string(expected)
}

func removeIfExists(path string) {
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

func (m *Manager) record(level string, kind string, message string) {
	if m.recordEvent == nil {
		return
	}
	m.recordEvent(level, kind, message)
}
