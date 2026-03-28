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
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/routing"
	"xiomi-router-driver/internal/runtimebin"
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
}

type desiredInstance struct {
	Key      string
	Provider config.Provider
	Location string
	Domains  []string
	Entry    Entry
}

type managedInstance struct {
	Key           string
	ProviderID    string
	ProviderName  string
	Location      string
	InterfaceName string
	DomainCount   int
	ConfigPath    string
	Settings      config.RoutingSettings
	PID           int
	cmd           *exec.Cmd
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

func (m *Manager) Apply(ctx context.Context, state config.State, enabledRules []config.Rule) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureReadyLocked(); err != nil {
		return err
	}

	if err := m.cleanupLocked(context.Background()); err != nil {
		return err
	}

	desired, err := m.buildDesired(state, enabledRules)
	if err != nil {
		return err
	}
	if len(desired) == 0 {
		return m.pruneRuntimeFilesLocked()
	}

	for index, item := range desired {
		instance, err := m.startDesiredInstance(item, state.Routing, index)
		if err != nil {
			_ = m.cleanupLocked(context.Background())
			return err
		}

		m.current[item.Key] = instance
		if err := m.saveInstanceLocked(instance); err != nil {
			_ = m.cleanupLocked(context.Background())
			return err
		}

		if err := waitForInterface(instance.Settings.VPNIface, 5*time.Second); err != nil {
			_ = m.cleanupLocked(context.Background())
			return fmt.Errorf("wait for %s interface: %w", item.Location, err)
		}

		if err := m.routing.RunWithOptions(ctx, "add", routing.RunOptions{
			Settings:       instance.Settings,
			DomainListPath: instance.domainListPath,
		}); err != nil {
			_ = m.cleanupLocked(context.Background())
			return fmt.Errorf("apply routing for %s: %w", item.Location, err)
		}

		removeIfExists(instance.domainListPath)
		instance.domainListPath = ""
	}

	return m.pruneRuntimeFilesLocked()
}

func (m *Manager) Cleanup(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureReadyLocked(); err != nil {
		return err
	}

	return m.cleanupLocked(ctx)
}

func (m *Manager) Snapshots() ([]RuntimeSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureReadyLocked(); err != nil {
		return nil, err
	}

	instances, err := m.loadInstancesLocked()
	if err != nil {
		return nil, err
	}

	snapshots := make([]RuntimeSnapshot, 0, len(instances))
	for _, instance := range instances {
		if instance == nil {
			continue
		}
		snapshots = append(snapshots, snapshotFromInstance(instance))
	}

	sort.Slice(snapshots, func(i, j int) bool {
		if snapshots[i].ProviderName == snapshots[j].ProviderName {
			return snapshots[i].Location < snapshots[j].Location
		}
		return snapshots[i].ProviderName < snapshots[j].ProviderName
	})

	return snapshots, nil
}

func (m *Manager) startDesiredInstance(item desiredInstance, base config.RoutingSettings, index int) (*managedInstance, error) {
	hash := shortHash(item.Provider.ID + "\n" + item.Location)
	settings := deriveRoutingSettings(base, hash, index)
	domainListPath := filepath.Join(m.runtimeDir, hash+".domains.list")
	configPath := filepath.Join(m.runtimeDir, hash+".json")

	if err := writeDomainList(domainListPath, item.Domains); err != nil {
		return nil, fmt.Errorf("write domain list for %s: %w", item.Location, err)
	}

	configData, err := json.MarshalIndent(buildSingBoxConfig(settings.VPNIface, index, item.Entry.Outbound), "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal sing-box config for %s: %w", item.Location, err)
	}
	configData = append(configData, '\n')
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		return nil, fmt.Errorf("write sing-box config for %s: %w", item.Location, err)
	}

	cmd := exec.Command(m.singBoxBinary, "run", "-c", configPath)
	cmd.Dir = m.runtimeDir

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		removeIfExists(domainListPath)
		removeIfExists(configPath)
		return nil, fmt.Errorf("prepare stdout for %s: %w", item.Location, err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		removeIfExists(domainListPath)
		removeIfExists(configPath)
		return nil, fmt.Errorf("prepare stderr for %s: %w", item.Location, err)
	}

	if err := cmd.Start(); err != nil {
		removeIfExists(domainListPath)
		removeIfExists(configPath)
		return nil, fmt.Errorf("start sing-box for %s: %w", item.Location, err)
	}

	instance := &managedInstance{
		Key:            item.Key,
		ProviderID:     item.Provider.ID,
		ProviderName:   item.Provider.Name,
		Location:       item.Location,
		InterfaceName:  settings.VPNIface,
		DomainCount:    len(item.Domains),
		ConfigPath:     configPath,
		Settings:       settings,
		PID:            cmd.Process.Pid,
		cmd:            cmd,
		domainListPath: domainListPath,
	}

	go m.streamRuntimeLogs(item.Provider.Name, item.Location, stdoutPipe)
	go m.streamRuntimeLogs(item.Provider.Name, item.Location, stderrPipe)
	go m.watchInstance(item.Key, cmd)

	m.record("info", "subscription.runtime_started", fmt.Sprintf("%s: %s started on %s", item.Provider.Name, item.Location, settings.VPNIface))
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
	err := cmd.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()

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
			process, findErr := os.FindProcess(instance.PID)
			if findErr == nil {
				_ = process.Kill()
			}
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

func (m *Manager) buildDesired(state config.State, enabledRules []config.Rule) ([]desiredInstance, error) {
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

	entriesByProvider := make(map[string][]Entry, len(groups))
	for _, group := range groups {
		if _, loaded := entriesByProvider[group.Provider.ID]; loaded {
			continue
		}
		entries, err := FetchEntries(group.Provider.Source)
		if err != nil {
			return nil, fmt.Errorf("load subscription %q: %w", group.Provider.Name, err)
		}
		entriesByProvider[group.Provider.ID] = entries
	}

	desired := make([]desiredInstance, 0, len(groups))
	for _, group := range groups {
		entry, found := findEntry(entriesByProvider[group.Provider.ID], group.Location)
		if !found {
			return nil, fmt.Errorf("location %q not found in provider %q", group.Location, group.Provider.Name)
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
	return os.WriteFile(path, []byte(content), 0o644)
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
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := net.InterfaceByName(name); err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("interface %s did not appear", name)
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

func snapshotFromInstance(instance *managedInstance) RuntimeSnapshot {
	status := "stopped"
	if _, err := net.InterfaceByName(instance.InterfaceName); err == nil {
		status = "running"
	}

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
		Status:        status,
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
	if level != "info" {
		return true
	}

	lower := strings.ToLower(strings.TrimSpace(line))
	return !strings.Contains(lower, "trace") && !strings.Contains(lower, "debug")
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
