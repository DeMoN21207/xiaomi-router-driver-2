package subscription

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (m *Manager) ensureReadyLocked() error {
	if m.initialized {
		return m.initErr
	}
	m.initialized = true

	if m.db == nil {
		m.initErr = errors.New("subscription runtime database is not configured")
		return m.initErr
	}

	if err := os.MkdirAll(m.runtimeDir, 0o755); err != nil {
		m.initErr = fmt.Errorf("prepare subscription runtime dir: %w", err)
		return m.initErr
	}

	if err := ensureRuntimeSchema(m.db); err != nil {
		m.initErr = err
		return err
	}

	if err := m.migrateLegacyManifestLocked(); err != nil {
		m.initErr = err
		return err
	}

	if err := m.pruneRuntimeFilesLocked(); err != nil {
		m.initErr = err
		return err
	}

	return nil
}

func ensureRuntimeSchema(db *sql.DB) error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS subscription_runtime_instances (
			key TEXT PRIMARY KEY,
			provider_id TEXT NOT NULL,
			provider_name TEXT NOT NULL,
			location TEXT NOT NULL,
			interface_name TEXT NOT NULL,
			domain_count INTEGER NOT NULL,
			config_path TEXT NOT NULL,
			settings_json TEXT NOT NULL,
			pid INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_subscription_runtime_provider ON subscription_runtime_instances(provider_name, location)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}

	return nil
}

func (m *Manager) migrateLegacyManifestLocked() error {
	var count int
	if err := m.db.QueryRow(`SELECT COUNT(1) FROM subscription_runtime_instances`).Scan(&count); err != nil || count > 0 {
		return err
	}

	data, err := os.ReadFile(m.legacyManifestPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read legacy subscription runtime manifest: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}

	var saved manifest
	if err := json.Unmarshal(data, &saved); err != nil {
		return fmt.Errorf("decode legacy subscription runtime manifest: %w", err)
	}

	for _, instance := range saved.Instances {
		if instance == nil || strings.TrimSpace(instance.Key) == "" {
			continue
		}
		if err := m.saveInstanceLocked(&managedInstance{
			Key:           instance.Key,
			ProviderID:    instance.ProviderID,
			ProviderName:  instance.ProviderName,
			Location:      instance.Location,
			InterfaceName: instance.InterfaceName,
			DomainCount:   countDomainEntries(instance.DomainListPath),
			ConfigPath:    instance.ConfigPath,
			Settings:      instance.Settings,
			PID:           instance.PID,
		}); err != nil {
			return err
		}
	}

	return nil
}

func (m *Manager) loadInstancesLocked() ([]*managedInstance, error) {
	rows, err := m.db.Query(`
		SELECT key, provider_id, provider_name, location, interface_name, domain_count, config_path, settings_json, pid
		FROM subscription_runtime_instances
		ORDER BY provider_name ASC, location ASC, key ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	instances := make([]*managedInstance, 0, 8)
	for rows.Next() {
		instance, err := scanRuntimeInstance(rows)
		if err != nil {
			return nil, err
		}
		instances = append(instances, instance)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return instances, nil
}

func scanRuntimeInstance(scanner interface{ Scan(dest ...any) error }) (*managedInstance, error) {
	var instance managedInstance
	var settingsJSON string

	if err := scanner.Scan(
		&instance.Key,
		&instance.ProviderID,
		&instance.ProviderName,
		&instance.Location,
		&instance.InterfaceName,
		&instance.DomainCount,
		&instance.ConfigPath,
		&settingsJSON,
		&instance.PID,
	); err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(settingsJSON), &instance.Settings); err != nil {
		return nil, fmt.Errorf("decode runtime settings: %w", err)
	}

	return &instance, nil
}

func (m *Manager) saveInstanceLocked(instance *managedInstance) error {
	if instance == nil {
		return nil
	}

	settingsJSON, err := json.Marshal(instance.Settings)
	if err != nil {
		return fmt.Errorf("encode runtime settings: %w", err)
	}

	_, err = m.db.Exec(`
		INSERT INTO subscription_runtime_instances (
			key, provider_id, provider_name, location, interface_name, domain_count, config_path, settings_json, pid
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			provider_id = excluded.provider_id,
			provider_name = excluded.provider_name,
			location = excluded.location,
			interface_name = excluded.interface_name,
			domain_count = excluded.domain_count,
			config_path = excluded.config_path,
			settings_json = excluded.settings_json,
			pid = excluded.pid
	`, instance.Key, instance.ProviderID, instance.ProviderName, instance.Location, instance.InterfaceName, instance.DomainCount, instance.ConfigPath, string(settingsJSON), instance.PID)
	return err
}

func (m *Manager) deleteInstanceLocked(key string) error {
	_, err := m.db.Exec(`DELETE FROM subscription_runtime_instances WHERE key = ?`, key)
	return err
}

func (m *Manager) clearInstancesLocked() error {
	_, err := m.db.Exec(`DELETE FROM subscription_runtime_instances`)
	return err
}

func (m *Manager) pruneRuntimeFilesLocked() error {
	instances, err := m.loadInstancesLocked()
	if err != nil {
		return err
	}

	keepConfigs := make(map[string]struct{}, len(instances))
	for _, instance := range instances {
		if instance == nil {
			continue
		}
		configPath := strings.TrimSpace(instance.ConfigPath)
		if configPath == "" {
			continue
		}
		keepConfigs[filepath.Clean(configPath)] = struct{}{}
	}

	patterns := []string{"*.domains.list", "*.log", "runtime.json"}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(m.runtimeDir, pattern))
		if err != nil {
			return err
		}
		for _, match := range matches {
			removeIfExists(match)
		}
	}

	configMatches, err := filepath.Glob(filepath.Join(m.runtimeDir, "*.json"))
	if err != nil {
		return err
	}
	for _, match := range configMatches {
		if _, keep := keepConfigs[filepath.Clean(match)]; keep {
			continue
		}
		removeIfExists(match)
	}

	return nil
}
