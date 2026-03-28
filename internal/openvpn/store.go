package openvpn

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func (m *Manager) ensureReadyLocked() error {
	if m.initialized {
		return m.initErr
	}
	m.initialized = true

	if m.db == nil {
		m.initErr = errors.New("openvpn runtime database is not configured")
		return m.initErr
	}

	if err := os.MkdirAll(m.runtimeDir, 0o755); err != nil {
		m.initErr = fmt.Errorf("prepare openvpn runtime dir: %w", err)
		return m.initErr
	}

	if err := ensureRuntimeSchema(m.db); err != nil {
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
		`CREATE TABLE IF NOT EXISTS openvpn_runtime_instances (
			provider_id TEXT PRIMARY KEY,
			provider_name TEXT NOT NULL,
			interface_name TEXT NOT NULL,
			profile_path TEXT NOT NULL,
			domain_count INTEGER NOT NULL,
			settings_json TEXT NOT NULL,
			pid INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_openvpn_runtime_provider ON openvpn_runtime_instances(provider_name, interface_name)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}

	return nil
}

func (m *Manager) loadInstancesLocked() ([]*managedInstance, error) {
	rows, err := m.db.Query(`
		SELECT provider_id, provider_name, interface_name, profile_path, domain_count, settings_json, pid
		FROM openvpn_runtime_instances
		ORDER BY provider_name ASC, provider_id ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	instances := make([]*managedInstance, 0, 4)
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
		&instance.ProviderID,
		&instance.ProviderName,
		&instance.InterfaceName,
		&instance.ProfilePath,
		&instance.DomainCount,
		&settingsJSON,
		&instance.PID,
	); err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(settingsJSON), &instance.Settings); err != nil {
		return nil, fmt.Errorf("decode openvpn runtime settings: %w", err)
	}

	return &instance, nil
}

func (m *Manager) saveInstanceLocked(instance *managedInstance) error {
	if instance == nil {
		return nil
	}

	settingsJSON, err := json.Marshal(instance.Settings)
	if err != nil {
		return fmt.Errorf("encode openvpn runtime settings: %w", err)
	}

	_, err = m.db.Exec(`
		INSERT INTO openvpn_runtime_instances (
			provider_id, provider_name, interface_name, profile_path, domain_count, settings_json, pid
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider_id) DO UPDATE SET
			provider_name = excluded.provider_name,
			interface_name = excluded.interface_name,
			profile_path = excluded.profile_path,
			domain_count = excluded.domain_count,
			settings_json = excluded.settings_json,
			pid = excluded.pid
	`, instance.ProviderID, instance.ProviderName, instance.InterfaceName, instance.ProfilePath, instance.DomainCount, string(settingsJSON), instance.PID)
	return err
}

func (m *Manager) deleteInstanceLocked(providerID string) error {
	_, err := m.db.Exec(`DELETE FROM openvpn_runtime_instances WHERE provider_id = ?`, providerID)
	return err
}

func (m *Manager) clearInstancesLocked() error {
	_, err := m.db.Exec(`DELETE FROM openvpn_runtime_instances`)
	return err
}

func (m *Manager) pruneRuntimeFilesLocked() error {
	matches, err := filepath.Glob(filepath.Join(m.runtimeDir, "*.domains.list"))
	if err != nil {
		return err
	}
	for _, match := range matches {
		removeIfExists(match)
	}
	return nil
}
