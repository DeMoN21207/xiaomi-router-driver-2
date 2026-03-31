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
}

type AutomationSettings struct {
	InstallService     bool `json:"installService"`
	AutoRecover        bool `json:"autoRecover"`
	TrafficCleanupDays int  `json:"trafficCleanupDays"`
}

type State struct {
	Providers     []Provider         `json:"providers"`
	Rules         []Rule             `json:"rules"`
	Routing       RoutingSettings    `json:"routing"`
	Automation    AutomationSettings `json:"automation"`
	LastAppliedAt string             `json:"lastAppliedAt"`
	LastError     string             `json:"lastError"`
	UpdatedAt     string             `json:"updatedAt"`
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
		Providers:  []Provider{},
		Rules:      []Rule{},
		Routing:    DefaultRoutingSettings(),
		Automation: DefaultAutomationSettings(),
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
	}
}

func DefaultAutomationSettings() AutomationSettings {
	return AutomationSettings{
		InstallService:     false,
		AutoRecover:        false,
		TrafficCleanupDays: 0,
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
			dnsmasq_config_file TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS automation_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			install_service INTEGER NOT NULL,
			auto_recover INTEGER NOT NULL,
			traffic_cleanup_days INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS app_meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_providers_position ON providers(position)`,
		`CREATE INDEX IF NOT EXISTS idx_rules_position ON rules(position)`,
		`CREATE INDEX IF NOT EXISTS idx_rule_domains_rule_position ON rule_domains(rule_id, position)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}

	// Add traffic_cleanup_days column if missing (existing databases).
	_, _ = db.Exec(`ALTER TABLE automation_settings ADD COLUMN traffic_cleanup_days INTEGER NOT NULL DEFAULT 0`)

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

	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}

	return normalize(state), nil
}

func saveStateTx(tx *sql.Tx, state State) error {
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

	if _, err := tx.Exec(`
		INSERT INTO routing_settings (
			id, vpn_gateway, vpn_route_mode, vpn_masquerade, lan_iface, vpn_iface,
			table_num, fw_zone_chain, ip_set_name, fw_mark, dnsmasq_config_file
		) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
			dnsmasq_config_file = excluded.dnsmasq_config_file
	`, state.Routing.VPNGateway, state.Routing.VPNRouteMode, boolToInt(state.Routing.VPNMasquerade), state.Routing.LANIface, state.Routing.VPNIface, state.Routing.TableNum, state.Routing.FWZoneChain, state.Routing.IPSetName, state.Routing.FWMark, state.Routing.DNSMasqConfigFile); err != nil {
		return err
	}

	if _, err := tx.Exec(`
		INSERT INTO automation_settings (id, install_service, auto_recover, traffic_cleanup_days)
		VALUES (1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			install_service = excluded.install_service,
			auto_recover = excluded.auto_recover,
			traffic_cleanup_days = excluded.traffic_cleanup_days
	`, boolToInt(state.Automation.InstallService), boolToInt(state.Automation.AutoRecover), state.Automation.TrafficCleanupDays); err != nil {
		return err
	}

	for key, value := range map[string]string{
		"lastAppliedAt": state.LastAppliedAt,
		"lastError":     state.LastError,
		"updatedAt":     state.UpdatedAt,
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

	var routing RoutingSettings
	var vpnMasquerade int
	err = m.db.QueryRow(`
		SELECT vpn_gateway, vpn_route_mode, vpn_masquerade, lan_iface, vpn_iface,
		       table_num, fw_zone_chain, ip_set_name, fw_mark, dnsmasq_config_file
		FROM routing_settings
		WHERE id = 1
	`).Scan(&routing.VPNGateway, &routing.VPNRouteMode, &vpnMasquerade, &routing.LANIface, &routing.VPNIface, &routing.TableNum, &routing.FWZoneChain, &routing.IPSetName, &routing.FWMark, &routing.DNSMasqConfigFile)
	switch {
	case err == nil:
		routing.VPNMasquerade = intToBool(vpnMasquerade)
		state.Routing = routing
	case errors.Is(err, sql.ErrNoRows):
	default:
		return State{}, err
	}

	var automation AutomationSettings
	var installService, autoRecover, trafficCleanupDays int
	err = m.db.QueryRow(`
		SELECT install_service, auto_recover, COALESCE(traffic_cleanup_days, 0)
		FROM automation_settings
		WHERE id = 1
	`).Scan(&installService, &autoRecover, &trafficCleanupDays)
	switch {
	case err == nil:
		automation.InstallService = intToBool(installService)
		automation.AutoRecover = intToBool(autoRecover)
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

func normalize(state State) State {
	state.Providers = normalizeProviders(state.Providers)
	state.Rules = normalizeRules(state.Rules)
	state.Routing = normalizeRoutingSettings(state.Routing)
	state.Automation = normalizeAutomationSettings(state.Automation)
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

	return settings
}

func normalizeAutomationSettings(settings AutomationSettings) AutomationSettings {
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
