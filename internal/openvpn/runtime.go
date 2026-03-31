package openvpn

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/routing"
	"xiomi-router-driver/internal/runtimebin"
	"xiomi-router-driver/internal/runtimehealth"
)

type Manager struct {
	db            *sql.DB
	dataDir       string
	runtimeDir    string
	openvpnBinary string
	routing       *routing.Runner
	recordEvent   func(level string, kind string, message string)
	mu            sync.Mutex
	current       map[string]*managedInstance
	initialized   bool
	initErr       error
}

type managedInstance struct {
	ProviderID     string
	ProviderName   string
	InterfaceName  string
	ProfilePath    string
	DomainCount    int
	Settings       config.RoutingSettings
	PID            int
	cmd            *exec.Cmd
	domainListPath string
}

type RuntimeSnapshot struct {
	ProviderID    string `json:"providerId"`
	ProviderName  string `json:"providerName"`
	InterfaceName string `json:"interfaceName"`
	ProfilePath   string `json:"profilePath"`
	TableNum      int    `json:"tableNum"`
	FWMark        string `json:"fwMark"`
	IPSetName     string `json:"ipSetName"`
	DNSMasqConfig string `json:"dnsMasqConfig"`
	DomainCount   int    `json:"domainCount"`
	PID           int    `json:"pid"`
	Status        string `json:"status"`
	StatusDetail  string `json:"statusDetail,omitempty"`
}

func NewManager(appDir string, dataDir string, db *sql.DB, routingRunner *routing.Runner, recordEvent func(level string, kind string, message string)) *Manager {
	openvpnBinary := runtimebin.Resolve(os.Getenv("VPN_MANAGER_OPENVPN_BIN"), "openvpn", appDir, dataDir)

	return &Manager{
		db:            db,
		dataDir:       dataDir,
		runtimeDir:    filepath.Join(dataDir, ".vpn-manager", "openvpn"),
		openvpnBinary: openvpnBinary,
		routing:       routingRunner,
		recordEvent:   recordEvent,
		current:       make(map[string]*managedInstance),
	}
}

func (m *Manager) Apply(ctx context.Context, provider config.Provider, domains []string, settings config.RoutingSettings) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureReadyLocked(); err != nil {
		return err
	}

	profilePath, err := resolveProfilePath(provider.Source, m.dataDir)
	if err != nil {
		return err
	}

	normalizedDomains := normalizeDomains(domains)
	if len(normalizedDomains) == 0 {
		return m.cleanupLocked(ctx)
	}

	existing, err := m.loadReusableInstanceLocked(provider.ID, profilePath, settings)
	if err != nil {
		return err
	}
	if existing != nil {
		if err := m.applyRoutingToInstanceLocked(ctx, existing, provider, profilePath, settings, normalizedDomains); err != nil {
			_ = m.cleanupLocked(context.Background())
			return fmt.Errorf("apply openvpn routing for %s: %w", provider.Name, err)
		}
		m.record("info", "openvpn.routing_updated", fmt.Sprintf("%s routing updated on %s", provider.Name, settings.VPNIface))
		return nil
	}

	if err := m.cleanupLocked(context.Background()); err != nil {
		return err
	}

	openvpnBinary, err := exec.LookPath(strings.TrimSpace(m.openvpnBinary))
	if err != nil {
		return fmt.Errorf("openvpn binary is not available: %w", err)
	}

	domainListPath := filepath.Join(m.runtimeDir, "openvpn-"+provider.ID+".domains.list")
	if err := writeDomainList(domainListPath, normalizedDomains); err != nil {
		return fmt.Errorf("write openvpn domain list: %w", err)
	}

	if interfaceExists(settings.VPNIface) {
		instance := &managedInstance{
			ProviderID:     provider.ID,
			ProviderName:   provider.Name,
			InterfaceName:  settings.VPNIface,
			ProfilePath:    profilePath,
			DomainCount:    len(normalizedDomains),
			Settings:       settings,
			PID:            0,
			domainListPath: domainListPath,
		}
		m.current[provider.ID] = instance
		if err := m.saveInstanceLocked(instance); err != nil {
			removeIfExists(domainListPath)
			return err
		}

		if err := m.routing.RunWithOptions(ctx, "add", routing.RunOptions{
			Settings:       settings,
			DomainListPath: domainListPath,
		}); err != nil {
			_ = m.cleanupLocked(context.Background())
			return fmt.Errorf("apply openvpn routing for %s: %w", provider.Name, err)
		}

		removeIfExists(domainListPath)
		instance.domainListPath = ""
		if err := m.saveInstanceLocked(instance); err != nil {
			_ = m.cleanupLocked(context.Background())
			return err
		}

		m.record("info", "openvpn.runtime_started", fmt.Sprintf("%s attached to existing %s", provider.Name, settings.VPNIface))
		return nil
	}

	profileDir := filepath.Dir(profilePath)
	args := []string{"--cd", profileDir, "--config", filepath.Base(profilePath), "--route-noexec"}
	if iface := strings.TrimSpace(settings.VPNIface); iface != "" {
		args = append(args, "--dev", iface)
	}

	cmd := exec.CommandContext(ctx, openvpnBinary, args...)
	cmd.Dir = profileDir

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		removeIfExists(domainListPath)
		return fmt.Errorf("prepare openvpn stdout: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		removeIfExists(domainListPath)
		return fmt.Errorf("prepare openvpn stderr: %w", err)
	}

	if err := cmd.Start(); err != nil {
		removeIfExists(domainListPath)
		return fmt.Errorf("start openvpn for %s: %w", provider.Name, err)
	}

	instance := &managedInstance{
		ProviderID:     provider.ID,
		ProviderName:   provider.Name,
		InterfaceName:  settings.VPNIface,
		ProfilePath:    profilePath,
		DomainCount:    len(normalizedDomains),
		Settings:       settings,
		PID:            cmd.Process.Pid,
		cmd:            cmd,
		domainListPath: domainListPath,
	}
	m.current[provider.ID] = instance
	if err := m.saveInstanceLocked(instance); err != nil {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		removeIfExists(domainListPath)
		return err
	}

	go m.streamRuntimeLogs(provider.Name, stdoutPipe)
	go m.streamRuntimeLogs(provider.Name, stderrPipe)
	go m.watchInstance(provider.ID, cmd)

	if err := waitForInterface(settings.VPNIface, 20*time.Second); err != nil {
		_ = m.cleanupLocked(context.Background())
		return fmt.Errorf("wait for openvpn interface %s: %w", settings.VPNIface, err)
	}

	if err := m.routing.RunWithOptions(ctx, "add", routing.RunOptions{
		Settings:       settings,
		DomainListPath: domainListPath,
	}); err != nil {
		_ = m.cleanupLocked(context.Background())
		return fmt.Errorf("apply openvpn routing for %s: %w", provider.Name, err)
	}

	removeIfExists(domainListPath)
	instance.domainListPath = ""
	if err := m.saveInstanceLocked(instance); err != nil {
		_ = m.cleanupLocked(context.Background())
		return err
	}

	m.record("info", "openvpn.runtime_started", fmt.Sprintf("%s started on %s", provider.Name, settings.VPNIface))
	return nil
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
		snapshots = append(snapshots, m.snapshotFromInstance(instance))
	}

	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].ProviderName < snapshots[j].ProviderName
	})

	return snapshots, nil
}

func (m *Manager) cleanupLocked(ctx context.Context) error {
	instances, err := m.loadInstancesLocked()
	if err != nil {
		return err
	}

	var cleanupErrors []error
	handled := make(map[string]struct{}, len(instances))

	for _, instance := range instances {
		if instance == nil {
			continue
		}
		handled[instance.ProviderID] = struct{}{}

		if current := m.current[instance.ProviderID]; current != nil {
			delete(m.current, instance.ProviderID)
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
	}

	for providerID, current := range m.current {
		if current == nil {
			delete(m.current, providerID)
			continue
		}
		if _, exists := handled[providerID]; exists {
			continue
		}

		delete(m.current, providerID)
		removeIfExists(current.domainListPath)
		if current.cmd != nil && current.cmd.Process != nil {
			_ = current.cmd.Process.Kill()
		}

		if err := m.routing.RunWithOptions(ctx, "del", routing.RunOptions{
			Settings: current.Settings,
		}); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
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

func (m *Manager) watchInstance(providerID string, cmd *exec.Cmd) {
	err := cmd.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()

	current, exists := m.current[providerID]
	if !exists || current == nil {
		return
	}
	if current.PID != 0 && cmd.Process != nil && current.PID != cmd.Process.Pid {
		return
	}

	delete(m.current, providerID)
	_ = m.deleteInstanceLocked(providerID)
	removeIfExists(current.domainListPath)
	_ = m.routing.RunWithOptions(context.Background(), "del", routing.RunOptions{
		Settings: current.Settings,
	})
	_ = m.pruneRuntimeFilesLocked()

	if err != nil {
		m.record("error", "openvpn.runtime_failed", fmt.Sprintf("%s stopped unexpectedly on %s: %v", current.ProviderName, current.InterfaceName, err))
	}
}

func (m *Manager) streamRuntimeLogs(providerName string, reader io.Reader) {
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
		m.record(level, "openvpn.runtime_log", fmt.Sprintf("%s: %s", providerName, line))
	}
	if err := scanner.Err(); err != nil {
		m.record("warn", "openvpn.runtime_log", fmt.Sprintf("%s: log stream error: %v", providerName, err))
	}
}

func (m *Manager) snapshotFromInstance(instance *managedInstance) RuntimeSnapshot {
	assessment := runtimehealth.Assess(runtimehealth.Check{
		InterfaceName:        instance.InterfaceName,
		PID:                  instance.PID,
		ProcessMarkers:       []string{m.openvpnBinary, instance.ProfilePath, instance.InterfaceName},
		EnableInterfaceProbe: true,
	})

	return RuntimeSnapshot{
		ProviderID:    instance.ProviderID,
		ProviderName:  instance.ProviderName,
		InterfaceName: instance.InterfaceName,
		ProfilePath:   instance.ProfilePath,
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

func resolveProfilePath(source string, dataDir string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", errors.New("openvpn provider source is empty")
	}

	profilePath := source
	if !filepath.IsAbs(profilePath) {
		profilePath = filepath.Join(dataDir, profilePath)
	}
	profilePath = filepath.Clean(profilePath)

	info, err := os.Stat(profilePath)
	if err != nil {
		return "", fmt.Errorf("open openvpn profile: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("openvpn profile path %q is a directory", profilePath)
	}

	return profilePath, nil
}

func normalizeDomains(domains []string) []string {
	out := make([]string, 0, len(domains))
	seen := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		domain = strings.TrimSpace(strings.ToLower(domain))
		if domain == "" {
			continue
		}
		if _, exists := seen[domain]; exists {
			continue
		}
		seen[domain] = struct{}{}
		out = append(out, domain)
	}
	return out
}

func writeDomainList(path string, domains []string) error {
	content := strings.Join(domains, "\n")
	if content != "" {
		content += "\n"
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func waitForInterface(name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if runtimehealth.InterfaceAlive(name) {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("interface %s did not appear", name)
}

func interfaceExists(name string) bool {
	return runtimehealth.InterfaceAlive(name)
}

func (m *Manager) loadReusableInstanceLocked(providerID string, profilePath string, settings config.RoutingSettings) (*managedInstance, error) {
	if current := m.current[providerID]; current != nil {
		if m.canReuseInstance(current, profilePath, settings) {
			return current, nil
		}
		return nil, nil
	}

	instances, err := m.loadInstancesLocked()
	if err != nil {
		return nil, err
	}
	for _, instance := range instances {
		if instance == nil || instance.ProviderID != providerID {
			continue
		}
		if m.canReuseInstance(instance, profilePath, settings) {
			return instance, nil
		}
		return nil, nil
	}

	return nil, nil
}

func (m *Manager) canReuseInstance(instance *managedInstance, profilePath string, settings config.RoutingSettings) bool {
	if !sameRuntimeConfig(instance, profilePath, settings) {
		return false
	}
	if !runtimehealth.InterfaceAlive(instance.InterfaceName) {
		return false
	}
	if instance.PID <= 0 {
		return true
	}
	return runtimehealth.ProcessAlive(instance.PID, m.openvpnBinary, instance.ProfilePath, instance.InterfaceName)
}

func (m *Manager) applyRoutingToInstanceLocked(ctx context.Context, instance *managedInstance, provider config.Provider, profilePath string, settings config.RoutingSettings, domains []string) error {
	target := instance
	if current := m.current[instance.ProviderID]; current != nil {
		target = current
	}

	domainListPath := filepath.Join(m.runtimeDir, "openvpn-"+target.ProviderID+".domains.list")
	if err := writeDomainList(domainListPath, domains); err != nil {
		return fmt.Errorf("write openvpn domain list: %w", err)
	}
	defer removeIfExists(domainListPath)

	if err := m.routing.RunWithOptions(ctx, "sync", routing.RunOptions{
		Settings:       settings,
		DomainListPath: domainListPath,
	}); err != nil {
		if err := m.routing.RunWithOptions(ctx, "add", routing.RunOptions{
			Settings:       settings,
			DomainListPath: domainListPath,
		}); err != nil {
			return err
		}
	}

	target.ProviderID = provider.ID
	target.ProviderName = provider.Name
	target.InterfaceName = settings.VPNIface
	target.ProfilePath = profilePath
	target.Settings = settings
	target.DomainCount = len(domains)
	target.domainListPath = ""
	return m.saveInstanceLocked(target)
}

func sameRuntimeConfig(instance *managedInstance, profilePath string, settings config.RoutingSettings) bool {
	if instance == nil {
		return false
	}
	return strings.TrimSpace(instance.InterfaceName) == strings.TrimSpace(settings.VPNIface) &&
		filepath.Clean(strings.TrimSpace(instance.ProfilePath)) == filepath.Clean(strings.TrimSpace(profilePath)) &&
		instance.Settings == settings
}

func runtimeLogLevel(line string) string {
	lower := strings.ToLower(strings.TrimSpace(line))
	switch {
	case strings.Contains(lower, "fatal"), strings.Contains(lower, "panic"), strings.Contains(lower, "error"), strings.Contains(lower, "exiting"):
		return "error"
	case strings.Contains(lower, "warn"):
		return "warn"
	default:
		return "info"
	}
}

func shouldRecordRuntimeLog(line string, level string) bool {
	if level == "info" {
		return false
	}

	lower := strings.ToLower(strings.TrimSpace(line))
	return !strings.Contains(lower, "debug")
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
