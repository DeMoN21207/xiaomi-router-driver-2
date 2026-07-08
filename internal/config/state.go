package config

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"xiomi-router-driver/internal/domains"
)

type ProviderType string

const (
	ProviderTypeOpenVPN      ProviderType = "openvpn"
	ProviderTypeSubscription ProviderType = "subscription"
)

type Provider struct {
	ID               string       `json:"id"`
	Name             string       `json:"name"`
	Type             ProviderType `json:"type"`
	Source           string       `json:"source"`
	SelectedLocation string       `json:"selectedLocation"`
	Enabled          bool         `json:"enabled"`
}

type Rule struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	ProviderID       string   `json:"providerId"`
	SelectedLocation string   `json:"selectedLocation"`
	Domains          []string `json:"domains"`
	Enabled          bool     `json:"enabled"`
}

type PriorityPolicy struct {
	ID         string                   `json:"id"`
	ProviderID string                   `json:"providerId"`
	Name       string                   `json:"name"`
	Enabled    bool                     `json:"enabled"`
	Entries    []string                 `json:"entries"`
	Targets    []PriorityTarget         `json:"targets"`
	Schedule   []PriorityScheduleWindow `json:"schedule"`
}

type PriorityTarget struct {
	Location string `json:"location"`
}

type PriorityScheduleWindow struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Location string `json:"location"`
}

type RoutingSettings struct {
	VPNGateway        string `json:"vpnGateway"`
	VPNRouteMode      string `json:"vpnRouteMode"`
	VPNMasquerade     bool   `json:"vpnMasquerade"`
	LANIface          string `json:"lanIface"`
	VPNIface          string `json:"vpnIface"`
	TableNum          int    `json:"tableNum"`
	FWZoneChain       string `json:"fwZoneChain"`
	IPSetName         string `json:"ipSetName"`
	FWMark            string `json:"fwMark"`
	DNSMasqConfigFile string `json:"dnsMasqConfigFile"`
	MSSClamp          bool   `json:"mssClamp"`
	MSSValue          int    `json:"mssValue"`
	DNSHijack         bool   `json:"dnsHijack"`
	IPv6Mode          string `json:"ipv6Mode"`
	LoadProfile       string `json:"loadProfile"`
}

type AutomationSettings struct {
	InstallService         bool   `json:"installService"`
	AutoRecover            bool   `json:"autoRecover"`
	ProviderFailover       bool   `json:"providerFailover"`
	FailoverFailureSeconds int    `json:"failoverFailureSeconds"`
	FailoverRestoreSeconds int    `json:"failoverRestoreSeconds"`
	FailoverAllDownMode    string `json:"failoverAllDownMode"`
	TrafficCleanupDays     int    `json:"trafficCleanupDays"`
}

type UpdateSettings struct {
	Repository   string `json:"repository"`
	AssetPattern string `json:"assetPattern"`
}

type State struct {
	Providers        []Provider         `json:"providers"`
	Rules            []Rule             `json:"rules"`
	PriorityPolicies []PriorityPolicy   `json:"priorityPolicies"`
	Routing          RoutingSettings    `json:"routing"`
	Automation       AutomationSettings `json:"automation"`
	Update           UpdateSettings     `json:"update"`
	LastAppliedAt    string             `json:"lastAppliedAt"`
	LastError        string             `json:"lastError"`
	UpdatedAt        string             `json:"updatedAt"`
}

type Manager struct {
	db          *sql.DB
	legacyPath  string
	mu          sync.Mutex
	initialized bool
	initErr     error
}

func NewManager(db *sql.DB, legacyPath string) *Manager {
	return &Manager{
		db:         db,
		legacyPath: strings.TrimSpace(legacyPath),
	}
}

func DefaultState() State {
	return State{
		Providers:        []Provider{},
		Rules:            []Rule{},
		PriorityPolicies: []PriorityPolicy{},
		Routing:          DefaultRoutingSettings(),
		Automation:       DefaultAutomationSettings(),
		Update:           DefaultUpdateSettings(),
	}
}

func DefaultRoutingSettings() RoutingSettings {
	return RoutingSettings{
		VPNGateway:        "10.8.0.1",
		VPNRouteMode:      "gateway",
		VPNMasquerade:     true,
		LANIface:          "br-lan",
		VPNIface:          "tun0",
		TableNum:          101,
		FWZoneChain:       "zone_lan_forward",
		IPSetName:         "vpn_hosts",
		FWMark:            "0x1",
		DNSMasqConfigFile: "/tmp/dnsmasq.d/vpn_dns.conf",
		MSSClamp:          true,
		MSSValue:          0,
		DNSHijack:         true,
		IPv6Mode:          "warn",
		LoadProfile:       DefaultRoutingLoadProfile(),
	}
}

func DefaultAutomationSettings() AutomationSettings {
	return AutomationSettings{
		InstallService:         false,
		AutoRecover:            false,
		ProviderFailover:       true,
		FailoverFailureSeconds: 120,
		FailoverRestoreSeconds: 60,
		FailoverAllDownMode:    "keep",
		TrafficCleanupDays:     14,
	}
}

func DefaultUpdateSettings() UpdateSettings {
	return UpdateSettings{
		Repository:   "DeMoN21207/xiaomi-router-driver-2",
		AssetPattern: "vpn-manager-linux-arm64.tar.gz",
	}
}

func (m *Manager) Load() (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureReadyUnlocked(); err != nil {
		return State{}, err
	}

	return m.loadUnlocked()
}

func (m *Manager) Save(state State) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureReadyUnlocked(); err != nil {
		return State{}, err
	}

	state = normalize(state)
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)

	tx, err := m.db.Begin()
	if err != nil {
		return State{}, err
	}

	if err := saveStateTx(tx, state); err != nil {
		_ = tx.Rollback()
		return State{}, err
	}

	if err := tx.Commit(); err != nil {
		return State{}, err
	}

	return state, nil
}

func (m *Manager) UpdateRule(rule Rule) (Rule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureReadyUnlocked(); err != nil {
		return Rule{}, err
	}

	normalized := normalizeRules([]Rule{rule})
	if len(normalized) == 0 {
		return Rule{}, errors.New("rule is invalid")
	}
	rule = normalized[0]
	updatedAt := time.Now().UTC().Format(time.RFC3339)

	tx, err := m.db.Begin()
	if err != nil {
		return Rule{}, err
	}

	result, err := tx.Exec(`
		UPDATE rules
		SET name = ?, provider_id = ?, selected_location = ?, enabled = ?
		WHERE id = ?
	`, rule.Name, rule.ProviderID, rule.SelectedLocation, boolToInt(rule.Enabled), rule.ID)
	if err != nil {
		_ = tx.Rollback()
		return Rule{}, err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return Rule{}, err
	}
	if rowsAffected == 0 {
		_ = tx.Rollback()
		return Rule{}, sql.ErrNoRows
	}

	if err := replaceRuleDomainsTx(tx, rule.ID, rule.Domains); err != nil {
		_ = tx.Rollback()
		return Rule{}, err
	}
	if err := saveMetaTx(tx, "updatedAt", updatedAt); err != nil {
		_ = tx.Rollback()
		return Rule{}, err
	}
	if err := tx.Commit(); err != nil {
		return Rule{}, err
	}

	return rule, nil
}

func (m *Manager) DeleteRule(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureReadyUnlocked(); err != nil {
		return err
	}

	id = strings.TrimSpace(id)
	if id == "" {
		return sql.ErrNoRows
	}

	tx, err := m.db.Begin()
	if err != nil {
		return err
	}

	var position int
	if err := tx.QueryRow(`SELECT position FROM rules WHERE id = ?`, id).Scan(&position); err != nil {
		_ = tx.Rollback()
		return err
	}

	if _, err := tx.Exec(`DELETE FROM rule_domains WHERE rule_id = ?`, id); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM rules WHERE id = ?`, id); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`UPDATE rules SET position = position - 1 WHERE position > ?`, position); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := saveMetaTx(tx, "updatedAt", time.Now().UTC().Format(time.RFC3339)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (m *Manager) ensureReadyUnlocked() error {
	if m.initialized {
		return m.initErr
	}
	m.initialized = true

	if m.db == nil {
		m.initErr = errors.New("config database is not configured")
		return m.initErr
	}

	if err := ensureStateSchema(m.db); err != nil {
		m.initErr = err
		return err
	}

	if err := m.migrateLegacyUnlocked(); err != nil {
		m.initErr = err
		return err
	}

	return nil
}

func ensureStateSchema(db *sql.DB) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS providers (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			source TEXT NOT NULL,
			selected_location TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			position INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS rules (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			provider_id TEXT NOT NULL,
			selected_location TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			position INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS rule_domains (
			rule_id TEXT NOT NULL,
			domain TEXT NOT NULL,
			position INTEGER NOT NULL,
			PRIMARY KEY (rule_id, domain),
			FOREIGN KEY (rule_id) REFERENCES rules(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS priority_policies (
			id TEXT PRIMARY KEY,
			provider_id TEXT NOT NULL,
			name TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			position INTEGER NOT NULL,
			FOREIGN KEY (provider_id) REFERENCES providers(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS priority_policy_entries (
			policy_id TEXT NOT NULL,
			entry TEXT NOT NULL,
			position INTEGER NOT NULL,
			PRIMARY KEY (policy_id, entry),
			FOREIGN KEY (policy_id) REFERENCES priority_policies(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS priority_policy_targets (
			policy_id TEXT NOT NULL,
			location TEXT NOT NULL,
			position INTEGER NOT NULL,
			PRIMARY KEY (policy_id, location),
			FOREIGN KEY (policy_id) REFERENCES priority_policies(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS priority_policy_schedule (
			policy_id TEXT NOT NULL,
			start_time TEXT NOT NULL,
			end_time TEXT NOT NULL,
			location TEXT NOT NULL,
			position INTEGER NOT NULL,
			FOREIGN KEY (policy_id) REFERENCES priority_policies(id) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS routing_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			vpn_gateway TEXT NOT NULL,
			vpn_route_mode TEXT NOT NULL,
			vpn_masquerade INTEGER NOT NULL,
			lan_iface TEXT NOT NULL,
			vpn_iface TEXT NOT NULL,
			table_num INTEGER NOT NULL,
			fw_zone_chain TEXT NOT NULL,
			ip_set_name TEXT NOT NULL,
			fw_mark TEXT NOT NULL,
			dnsmasq_config_file TEXT NOT NULL,
			mss_clamp INTEGER NOT NULL DEFAULT 1,
			mss_value INTEGER NOT NULL DEFAULT 0,
			dns_hijack INTEGER NOT NULL DEFAULT 1,
			ipv6_mode TEXT NOT NULL DEFAULT 'warn',
			load_profile TEXT NOT NULL DEFAULT 'normal'
		)`,
		`CREATE TABLE IF NOT EXISTS automation_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			install_service INTEGER NOT NULL,
			auto_recover INTEGER NOT NULL,
			provider_failover INTEGER NOT NULL DEFAULT 1,
			failover_failure_seconds INTEGER NOT NULL DEFAULT 120,
			failover_restore_seconds INTEGER NOT NULL DEFAULT 60,
			failover_all_down_mode TEXT NOT NULL DEFAULT 'keep',
			traffic_cleanup_days INTEGER NOT NULL DEFAULT 14
			)`,
		`CREATE TABLE IF NOT EXISTS app_meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_providers_position ON providers(position)`,
		`CREATE INDEX IF NOT EXISTS idx_rules_position ON rules(position)`,
		`CREATE INDEX IF NOT EXISTS idx_rule_domains_rule_position ON rule_domains(rule_id, position)`,
		`CREATE INDEX IF NOT EXISTS idx_priority_policies_provider_position ON priority_policies(provider_id, position)`,
		`CREATE INDEX IF NOT EXISTS idx_priority_policy_entries_position ON priority_policy_entries(policy_id, position)`,
		`CREATE INDEX IF NOT EXISTS idx_priority_policy_targets_position ON priority_policy_targets(policy_id, position)`,
		`CREATE INDEX IF NOT EXISTS idx_priority_policy_schedule_position ON priority_policy_schedule(policy_id, position)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}

	// Add columns if missing (existing databases).
	_, _ = db.Exec(`ALTER TABLE routing_settings ADD COLUMN mss_clamp INTEGER NOT NULL DEFAULT 1`)
	_, _ = db.Exec(`ALTER TABLE routing_settings ADD COLUMN mss_value INTEGER NOT NULL DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE routing_settings ADD COLUMN dns_hijack INTEGER NOT NULL DEFAULT 1`)
	_, _ = db.Exec(`ALTER TABLE routing_settings ADD COLUMN ipv6_mode TEXT NOT NULL DEFAULT 'warn'`)
	_, _ = db.Exec(`ALTER TABLE routing_settings ADD COLUMN load_profile TEXT NOT NULL DEFAULT 'normal'`)
	_, _ = db.Exec(`ALTER TABLE automation_settings ADD COLUMN traffic_cleanup_days INTEGER NOT NULL DEFAULT 14`)
	_, _ = db.Exec(`ALTER TABLE automation_settings ADD COLUMN provider_failover INTEGER NOT NULL DEFAULT 1`)
	_, _ = db.Exec(`ALTER TABLE automation_settings ADD COLUMN failover_failure_seconds INTEGER NOT NULL DEFAULT 120`)
	_, _ = db.Exec(`ALTER TABLE automation_settings ADD COLUMN failover_restore_seconds INTEGER NOT NULL DEFAULT 60`)
	_, _ = db.Exec(`ALTER TABLE automation_settings ADD COLUMN failover_all_down_mode TEXT NOT NULL DEFAULT 'keep'`)

	return nil
}

func (m *Manager) migrateLegacyUnlocked() error {
	hasData, err := stateDataPresent(m.db)
	if err != nil || hasData {
		return err
	}

	state, err := loadLegacyState(m.legacyPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	state = normalize(state)
	if strings.TrimSpace(state.UpdatedAt) == "" {
		state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	tx, err := m.db.Begin()
	if err != nil {
		return err
	}

	if err := saveStateTx(tx, state); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

func stateDataPresent(db *sql.DB) (bool, error) {
	var present int
	err := db.QueryRow(`
		SELECT
			EXISTS(SELECT 1 FROM providers LIMIT 1) OR
			EXISTS(SELECT 1 FROM rules LIMIT 1) OR
			EXISTS(SELECT 1 FROM priority_policies LIMIT 1) OR
			EXISTS(SELECT 1 FROM routing_settings LIMIT 1) OR
			EXISTS(SELECT 1 FROM automation_settings LIMIT 1) OR
			EXISTS(SELECT 1 FROM app_meta LIMIT 1)
	`).Scan(&present)
	return present != 0, err
}

func loadLegacyState(path string) (State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return State{}, err
	}

	if len(strings.TrimSpace(string(data))) == 0 {
		return DefaultState(), nil
	}

	state := DefaultState()
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}

	return normalize(state), nil
}

func saveStateTx(tx *sql.Tx, state State) error {
	if _, err := tx.Exec(`DELETE FROM priority_policy_schedule`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM priority_policy_targets`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM priority_policy_entries`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM priority_policies`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM rule_domains`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM rules`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM providers`); err != nil {
		return err
	}

	for index, provider := range state.Providers {
		if _, err := tx.Exec(`
			INSERT INTO providers (id, name, type, source, selected_location, enabled, position)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, provider.ID, provider.Name, string(provider.Type), provider.Source, provider.SelectedLocation, boolToInt(provider.Enabled), index); err != nil {
			return err
		}
	}

	for ruleIndex, rule := range state.Rules {
		if _, err := tx.Exec(`
			INSERT INTO rules (id, name, provider_id, selected_location, enabled, position)
			VALUES (?, ?, ?, ?, ?, ?)
		`, rule.ID, rule.Name, rule.ProviderID, rule.SelectedLocation, boolToInt(rule.Enabled), ruleIndex); err != nil {
			return err
		}

		if err := replaceRuleDomainsTx(tx, rule.ID, rule.Domains); err != nil {
			return err
		}
	}

	for policyIndex, policy := range state.PriorityPolicies {
		if _, err := tx.Exec(`
			INSERT INTO priority_policies (id, provider_id, name, enabled, position)
			VALUES (?, ?, ?, ?, ?)
		`, policy.ID, policy.ProviderID, policy.Name, boolToInt(policy.Enabled), policyIndex); err != nil {
			return err
		}
		if err := replacePriorityPolicyEntriesTx(tx, policy.ID, policy.Entries); err != nil {
			return err
		}
		if err := replacePriorityPolicyTargetsTx(tx, policy.ID, policy.Targets); err != nil {
			return err
		}
		if err := replacePriorityPolicyScheduleTx(tx, policy.ID, policy.Schedule); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(`
		INSERT INTO routing_settings (
			id, vpn_gateway, vpn_route_mode, vpn_masquerade, lan_iface, vpn_iface,
			table_num, fw_zone_chain, ip_set_name, fw_mark, dnsmasq_config_file,
			mss_clamp, mss_value, dns_hijack, ipv6_mode, load_profile
		) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			vpn_gateway = excluded.vpn_gateway,
			vpn_route_mode = excluded.vpn_route_mode,
			vpn_masquerade = excluded.vpn_masquerade,
			lan_iface = excluded.lan_iface,
			vpn_iface = excluded.vpn_iface,
			table_num = excluded.table_num,
			fw_zone_chain = excluded.fw_zone_chain,
			ip_set_name = excluded.ip_set_name,
			fw_mark = excluded.fw_mark,
			dnsmasq_config_file = excluded.dnsmasq_config_file,
			mss_clamp = excluded.mss_clamp,
			mss_value = excluded.mss_value,
			dns_hijack = excluded.dns_hijack,
			ipv6_mode = excluded.ipv6_mode,
			load_profile = excluded.load_profile
	`, state.Routing.VPNGateway, state.Routing.VPNRouteMode, boolToInt(state.Routing.VPNMasquerade), state.Routing.LANIface, state.Routing.VPNIface, state.Routing.TableNum, state.Routing.FWZoneChain, state.Routing.IPSetName, state.Routing.FWMark, state.Routing.DNSMasqConfigFile, boolToInt(state.Routing.MSSClamp), state.Routing.MSSValue, boolToInt(state.Routing.DNSHijack), state.Routing.IPv6Mode, state.Routing.LoadProfile); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		INSERT INTO automation_settings (
			id, install_service, auto_recover, provider_failover,
			failover_failure_seconds, failover_restore_seconds, failover_all_down_mode, traffic_cleanup_days
		) VALUES (1, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			install_service = excluded.install_service,
			auto_recover = excluded.auto_recover,
			provider_failover = excluded.provider_failover,
			failover_failure_seconds = excluded.failover_failure_seconds,
			failover_restore_seconds = excluded.failover_restore_seconds,
			failover_all_down_mode = excluded.failover_all_down_mode,
			traffic_cleanup_days = excluded.traffic_cleanup_days
	`, boolToInt(state.Automation.InstallService), boolToInt(state.Automation.AutoRecover), boolToInt(state.Automation.ProviderFailover), state.Automation.FailoverFailureSeconds, state.Automation.FailoverRestoreSeconds, state.Automation.FailoverAllDownMode, state.Automation.TrafficCleanupDays); err != nil {
		return err
	}

	for key, value := range map[string]string{
		"lastAppliedAt":      state.LastAppliedAt,
		"lastError":          state.LastError,
		"updatedAt":          state.UpdatedAt,
		"updateRepository":   state.Update.Repository,
		"updateAssetPattern": state.Update.AssetPattern,
	} {
		if err := saveMetaTx(tx, key, value); err != nil {
			return err
		}
	}

	return nil
}

func replaceRuleDomainsTx(tx *sql.Tx, ruleID string, domains []string) error {
	if _, err := tx.Exec(`DELETE FROM rule_domains WHERE rule_id = ?`, ruleID); err != nil {
		return err
	}

	for domainIndex, domain := range domains {
		if _, err := tx.Exec(`
			INSERT INTO rule_domains (rule_id, domain, position)
			VALUES (?, ?, ?)
		`, ruleID, domain, domainIndex); err != nil {
			return err
		}
	}

	return nil
}

func replacePriorityPolicyEntriesTx(tx *sql.Tx, policyID string, entries []string) error {
	if _, err := tx.Exec(`DELETE FROM priority_policy_entries WHERE policy_id = ?`, policyID); err != nil {
		return err
	}

	for index, entry := range entries {
		if _, err := tx.Exec(`
			INSERT INTO priority_policy_entries (policy_id, entry, position)
			VALUES (?, ?, ?)
		`, policyID, entry, index); err != nil {
			return err
		}
	}

	return nil
}

func replacePriorityPolicyTargetsTx(tx *sql.Tx, policyID string, targets []PriorityTarget) error {
	if _, err := tx.Exec(`DELETE FROM priority_policy_targets WHERE policy_id = ?`, policyID); err != nil {
		return err
	}

	for index, target := range targets {
		if _, err := tx.Exec(`
			INSERT INTO priority_policy_targets (policy_id, location, position)
			VALUES (?, ?, ?)
		`, policyID, target.Location, index); err != nil {
			return err
		}
	}

	return nil
}

func replacePriorityPolicyScheduleTx(tx *sql.Tx, policyID string, schedule []PriorityScheduleWindow) error {
	if _, err := tx.Exec(`DELETE FROM priority_policy_schedule WHERE policy_id = ?`, policyID); err != nil {
		return err
	}

	for index, window := range schedule {
		if _, err := tx.Exec(`
			INSERT INTO priority_policy_schedule (policy_id, start_time, end_time, location, position)
			VALUES (?, ?, ?, ?, ?)
		`, policyID, window.Start, window.End, window.Location, index); err != nil {
			return err
		}
	}

	return nil
}

func saveMetaTx(tx *sql.Tx, key string, value string) error {
	_, err := tx.Exec(`
		INSERT INTO app_meta (key, value)
		VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

func (m *Manager) loadUnlocked() (State, error) {
	state := DefaultState()

	providerRows, err := m.db.Query(`
		SELECT id, name, type, source, selected_location, enabled
		FROM providers
		ORDER BY position ASC, rowid ASC
	`)
	if err != nil {
		return State{}, err
	}
	defer providerRows.Close()

	for providerRows.Next() {
		var provider Provider
		var enabled int
		var providerType string
		if err := providerRows.Scan(&provider.ID, &provider.Name, &providerType, &provider.Source, &provider.SelectedLocation, &enabled); err != nil {
			return State{}, err
		}
		provider.Type = ProviderType(providerType)
		provider.Enabled = intToBool(enabled)
		state.Providers = append(state.Providers, provider)
	}
	if err := providerRows.Err(); err != nil {
		return State{}, err
	}

	ruleRows, err := m.db.Query(`
		SELECT id, name, provider_id, selected_location, enabled
		FROM rules
		ORDER BY position ASC, rowid ASC
	`)
	if err != nil {
		return State{}, err
	}
	defer ruleRows.Close()

	for ruleRows.Next() {
		var rule Rule
		var enabled int
		if err := ruleRows.Scan(&rule.ID, &rule.Name, &rule.ProviderID, &rule.SelectedLocation, &enabled); err != nil {
			return State{}, err
		}
		rule.Enabled = intToBool(enabled)
		state.Rules = append(state.Rules, rule)
	}
	if err := ruleRows.Err(); err != nil {
		return State{}, err
	}
	if err := ruleRows.Close(); err != nil {
		return State{}, err
	}

	for index := range state.Rules {
		state.Rules[index].Domains, err = loadRuleDomains(m.db, state.Rules[index].ID)
		if err != nil {
			return State{}, err
		}
	}

	policyRows, err := m.db.Query(`
		SELECT id, provider_id, name, enabled
		FROM priority_policies
		ORDER BY position ASC, rowid ASC
	`)
	if err != nil {
		return State{}, err
	}
	defer policyRows.Close()

	for policyRows.Next() {
		var policy PriorityPolicy
		var enabled int
		if err := policyRows.Scan(&policy.ID, &policy.ProviderID, &policy.Name, &enabled); err != nil {
			return State{}, err
		}
		policy.Enabled = intToBool(enabled)
		state.PriorityPolicies = append(state.PriorityPolicies, policy)
	}
	if err := policyRows.Err(); err != nil {
		return State{}, err
	}
	if err := policyRows.Close(); err != nil {
		return State{}, err
	}

	for index := range state.PriorityPolicies {
		state.PriorityPolicies[index].Entries, err = loadPriorityPolicyEntries(m.db, state.PriorityPolicies[index].ID)
		if err != nil {
			return State{}, err
		}
		state.PriorityPolicies[index].Targets, err = loadPriorityPolicyTargets(m.db, state.PriorityPolicies[index].ID)
		if err != nil {
			return State{}, err
		}
		state.PriorityPolicies[index].Schedule, err = loadPriorityPolicySchedule(m.db, state.PriorityPolicies[index].ID)
		if err != nil {
			return State{}, err
		}
	}

	var routing RoutingSettings
	var vpnMasquerade, mssClamp, mssValue, dnsHijack int
	err = m.db.QueryRow(`
		SELECT vpn_gateway, vpn_route_mode, vpn_masquerade, lan_iface, vpn_iface,
		       table_num, fw_zone_chain, ip_set_name, fw_mark, dnsmasq_config_file,
		       COALESCE(mss_clamp, 1), COALESCE(mss_value, 0), COALESCE(dns_hijack, 1),
		       COALESCE(ipv6_mode, 'warn'), COALESCE(load_profile, 'normal')
		FROM routing_settings
		WHERE id = 1
	`).Scan(&routing.VPNGateway, &routing.VPNRouteMode, &vpnMasquerade, &routing.LANIface, &routing.VPNIface, &routing.TableNum, &routing.FWZoneChain, &routing.IPSetName, &routing.FWMark, &routing.DNSMasqConfigFile, &mssClamp, &mssValue, &dnsHijack, &routing.IPv6Mode, &routing.LoadProfile)
	switch {
	case err == nil:
		routing.VPNMasquerade = intToBool(vpnMasquerade)
		routing.MSSClamp = intToBool(mssClamp)
		routing.MSSValue = mssValue
		routing.DNSHijack = intToBool(dnsHijack)
		state.Routing = routing
	case errors.Is(err, sql.ErrNoRows):
	default:
		return State{}, err
	}

	var automation AutomationSettings
	var installService, autoRecover, providerFailover, failoverFailureSeconds, failoverRestoreSeconds, trafficCleanupDays int
	var failoverAllDownMode string
	err = m.db.QueryRow(`
		SELECT install_service, auto_recover,
		       COALESCE(provider_failover, 1),
		       COALESCE(failover_failure_seconds, 120),
		       COALESCE(failover_restore_seconds, 60),
		COALESCE(failover_all_down_mode, 'keep'),
		       COALESCE(traffic_cleanup_days, 14)
		FROM automation_settings
		WHERE id = 1
	`).Scan(&installService, &autoRecover, &providerFailover, &failoverFailureSeconds, &failoverRestoreSeconds, &failoverAllDownMode, &trafficCleanupDays)
	switch {
	case err == nil:
		automation.InstallService = intToBool(installService)
		automation.AutoRecover = intToBool(autoRecover)
		automation.ProviderFailover = intToBool(providerFailover)
		automation.FailoverFailureSeconds = failoverFailureSeconds
		automation.FailoverRestoreSeconds = failoverRestoreSeconds
		automation.FailoverAllDownMode = failoverAllDownMode
		automation.TrafficCleanupDays = trafficCleanupDays
		state.Automation = automation
	case errors.Is(err, sql.ErrNoRows):
	default:
		return State{}, err
	}

	metaRows, err := m.db.Query(`SELECT key, value FROM app_meta`)
	if err != nil {
		return State{}, err
	}
	defer metaRows.Close()

	for metaRows.Next() {
		var key string
		var value string
		if err := metaRows.Scan(&key, &value); err != nil {
			return State{}, err
		}
		switch key {
		case "lastAppliedAt":
			state.LastAppliedAt = value
		case "lastError":
			state.LastError = value
		case "updatedAt":
			state.UpdatedAt = value
		case "updateRepository":
			state.Update.Repository = value
		case "updateAssetPattern":
			state.Update.AssetPattern = value
		}
	}
	if err := metaRows.Err(); err != nil {
		return State{}, err
	}

	return normalize(state), nil
}

func loadRuleDomains(db *sql.DB, ruleID string) ([]string, error) {
	rows, err := db.Query(`
		SELECT domain
		FROM rule_domains
		WHERE rule_id = ?
		ORDER BY position ASC, domain ASC
	`, ruleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	domains := make([]string, 0, 8)
	for rows.Next() {
		var domain string
		if err := rows.Scan(&domain); err != nil {
			return nil, err
		}
		domains = append(domains, domain)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return domains, nil
}

func loadPriorityPolicyEntries(db *sql.DB, policyID string) ([]string, error) {
	rows, err := db.Query(`
		SELECT entry
		FROM priority_policy_entries
		WHERE policy_id = ?
		ORDER BY position ASC, entry ASC
	`, policyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := make([]string, 0, 8)
	for rows.Next() {
		var entry string
		if err := rows.Scan(&entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return entries, nil
}

func loadPriorityPolicyTargets(db *sql.DB, policyID string) ([]PriorityTarget, error) {
	rows, err := db.Query(`
		SELECT location
		FROM priority_policy_targets
		WHERE policy_id = ?
		ORDER BY position ASC, location ASC
	`, policyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	targets := make([]PriorityTarget, 0, 4)
	for rows.Next() {
		var target PriorityTarget
		if err := rows.Scan(&target.Location); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return targets, nil
}

func loadPriorityPolicySchedule(db *sql.DB, policyID string) ([]PriorityScheduleWindow, error) {
	rows, err := db.Query(`
		SELECT start_time, end_time, location
		FROM priority_policy_schedule
		WHERE policy_id = ?
		ORDER BY position ASC, rowid ASC
	`, policyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	schedule := make([]PriorityScheduleWindow, 0, 4)
	for rows.Next() {
		var window PriorityScheduleWindow
		if err := rows.Scan(&window.Start, &window.End, &window.Location); err != nil {
			return nil, err
		}
		schedule = append(schedule, window)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return schedule, nil
}

func normalize(state State) State {
	state.Providers = normalizeProviders(state.Providers)
	state.Rules = normalizeRules(state.Rules)
	state.PriorityPolicies = normalizePriorityPolicies(state.PriorityPolicies)
	state.Routing = normalizeRoutingSettings(state.Routing)
	state.Automation = normalizeAutomationSettings(state.Automation)
	state.Update = normalizeUpdateSettings(state.Update)
	state.LastAppliedAt = strings.TrimSpace(state.LastAppliedAt)
	state.LastError = strings.TrimSpace(state.LastError)
	state.UpdatedAt = strings.TrimSpace(state.UpdatedAt)

	return state
}

func normalizeProviders(providers []Provider) []Provider {
	out := make([]Provider, 0, len(providers))
	for _, provider := range providers {
		provider.ID = strings.TrimSpace(provider.ID)
		provider.Name = strings.TrimSpace(provider.Name)
		provider.Type = ProviderType(strings.TrimSpace(strings.ToLower(string(provider.Type))))
		provider.Source = strings.TrimSpace(provider.Source)
		provider.SelectedLocation = strings.TrimSpace(provider.SelectedLocation)

		switch provider.Type {
		case ProviderTypeOpenVPN, ProviderTypeSubscription:
		default:
			continue
		}

		if provider.ID == "" || provider.Name == "" {
			continue
		}

		out = append(out, provider)
	}
	return out
}

func normalizeRules(rules []Rule) []Rule {
	out := make([]Rule, 0, len(rules))
	for _, rule := range rules {
		rule.ID = strings.TrimSpace(rule.ID)
		rule.Name = strings.TrimSpace(rule.Name)
		rule.ProviderID = strings.TrimSpace(rule.ProviderID)
		rule.SelectedLocation = strings.TrimSpace(rule.SelectedLocation)
		rule.Domains = normalizeDomains(rule.Domains)

		if rule.ID == "" || rule.Name == "" || rule.ProviderID == "" {
			continue
		}

		out = append(out, rule)
	}
	return out
}

func normalizePriorityPolicies(policies []PriorityPolicy) []PriorityPolicy {
	out := make([]PriorityPolicy, 0, len(policies))
	for _, policy := range policies {
		policy.ID = strings.TrimSpace(policy.ID)
		policy.ProviderID = strings.TrimSpace(policy.ProviderID)
		policy.Name = strings.TrimSpace(policy.Name)
		policy.Entries = normalizeDomains(policy.Entries)
		policy.Targets = normalizePriorityTargets(policy.Targets)
		policy.Schedule = normalizePrioritySchedule(policy.Schedule)

		if policy.ID == "" || policy.ProviderID == "" || policy.Name == "" {
			continue
		}

		out = append(out, policy)
	}
	return out
}

func normalizePriorityTargets(targets []PriorityTarget) []PriorityTarget {
	out := make([]PriorityTarget, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		target.Location = strings.TrimSpace(target.Location)
		if target.Location == "" {
			continue
		}
		key := strings.ToLower(target.Location)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, target)
	}
	return out
}

func normalizePrioritySchedule(schedule []PriorityScheduleWindow) []PriorityScheduleWindow {
	out := make([]PriorityScheduleWindow, 0, len(schedule))
	for _, window := range schedule {
		window.Start = strings.TrimSpace(window.Start)
		window.End = strings.TrimSpace(window.End)
		window.Location = strings.TrimSpace(window.Location)
		if window.Start == "" || window.End == "" || window.Location == "" {
			continue
		}
		out = append(out, window)
	}
	return out
}

func normalizeDomains(values []string) []string {
	return domains.NormalizeEntries(values)
}

func normalizeRoutingSettings(settings RoutingSettings) RoutingSettings {
	defaults := DefaultRoutingSettings()

	settings.VPNGateway = strings.TrimSpace(settings.VPNGateway)
	if settings.VPNGateway == "" {
		settings.VPNGateway = defaults.VPNGateway
	}

	settings.VPNRouteMode = strings.ToLower(strings.TrimSpace(settings.VPNRouteMode))
	switch settings.VPNRouteMode {
	case "gateway", "dev":
	default:
		settings.VPNRouteMode = defaults.VPNRouteMode
	}

	settings.LANIface = strings.TrimSpace(settings.LANIface)
	if settings.LANIface == "" {
		settings.LANIface = defaults.LANIface
	}

	settings.VPNIface = strings.TrimSpace(settings.VPNIface)
	if settings.VPNIface == "" {
		settings.VPNIface = defaults.VPNIface
	}

	if settings.TableNum <= 0 {
		settings.TableNum = defaults.TableNum
	}

	settings.FWZoneChain = strings.TrimSpace(settings.FWZoneChain)
	if settings.FWZoneChain == "" {
		settings.FWZoneChain = defaults.FWZoneChain
	}

	settings.IPSetName = strings.TrimSpace(settings.IPSetName)
	if settings.IPSetName == "" {
		settings.IPSetName = defaults.IPSetName
	}

	settings.FWMark = strings.TrimSpace(settings.FWMark)
	if settings.FWMark == "" {
		settings.FWMark = defaults.FWMark
	}

	settings.DNSMasqConfigFile = strings.TrimSpace(settings.DNSMasqConfigFile)
	if settings.DNSMasqConfigFile == "" {
		settings.DNSMasqConfigFile = defaults.DNSMasqConfigFile
	}

	if settings.MSSValue < 0 {
		settings.MSSValue = 0
	}
	if settings.MSSValue > 1460 {
		settings.MSSValue = 1460
	}

	settings.IPv6Mode = strings.ToLower(strings.TrimSpace(settings.IPv6Mode))
	switch settings.IPv6Mode {
	case "warn", "allow", "disable":
	default:
		settings.IPv6Mode = defaults.IPv6Mode
	}

	settings.LoadProfile = NormalizeRoutingLoadProfile(settings.LoadProfile)

	return settings
}

func normalizeAutomationSettings(settings AutomationSettings) AutomationSettings {
	defaults := DefaultAutomationSettings()

	if settings.FailoverFailureSeconds <= 0 {
		settings.FailoverFailureSeconds = defaults.FailoverFailureSeconds
	}
	if settings.FailoverFailureSeconds < 30 {
		settings.FailoverFailureSeconds = 30
	}
	if settings.FailoverFailureSeconds > 3600 {
		settings.FailoverFailureSeconds = 3600
	}

	if settings.FailoverRestoreSeconds <= 0 {
		settings.FailoverRestoreSeconds = defaults.FailoverRestoreSeconds
	}
	if settings.FailoverRestoreSeconds < 10 {
		settings.FailoverRestoreSeconds = 10
	}
	if settings.FailoverRestoreSeconds > 3600 {
		settings.FailoverRestoreSeconds = 3600
	}

	settings.FailoverAllDownMode = strings.ToLower(strings.TrimSpace(settings.FailoverAllDownMode))
	switch settings.FailoverAllDownMode {
	case "keep":
	default:
		settings.FailoverAllDownMode = defaults.FailoverAllDownMode
	}

	if settings.TrafficCleanupDays < 0 {
		settings.TrafficCleanupDays = 0
	}

	return settings
}

func normalizeUpdateSettings(settings UpdateSettings) UpdateSettings {
	defaults := DefaultUpdateSettings()

	settings.Repository = strings.TrimSpace(settings.Repository)
	if settings.Repository == "" {
		settings.Repository = defaults.Repository
	}
	settings.Repository = strings.TrimPrefix(settings.Repository, "https://github.com/")
	settings.Repository = strings.TrimSuffix(settings.Repository, ".git")
	settings.Repository = strings.Trim(settings.Repository, "/")
	if parts := strings.Split(settings.Repository, "/"); len(parts) >= 2 {
		settings.Repository = parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	if settings.Repository == "" || !strings.Contains(settings.Repository, "/") {
		settings.Repository = defaults.Repository
	}

	settings.AssetPattern = strings.TrimSpace(settings.AssetPattern)
	if settings.AssetPattern == "" {
		settings.AssetPattern = defaults.AssetPattern
	}

	return settings
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func intToBool(value int) bool {
	return value != 0
}
