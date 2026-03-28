package blacklist

import (
	"database/sql"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

var domainPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

type Entry struct {
	Value    string `json:"value"`
	Type     string `json:"type"` // "ip" or "domain"
	Position int    `json:"-"`
}

type Manager struct {
	db              *sql.DB
	domainsListPath string
	ipsListPath     string
	mu              sync.Mutex
	initialized     bool
	initErr         error
}

func NewManager(db *sql.DB, runtimeDir string) *Manager {
	return &Manager{
		db:              db,
		domainsListPath: filepath.Join(runtimeDir, "blacklist_domains.list"),
		ipsListPath:     filepath.Join(runtimeDir, "blacklist_ips.list"),
	}
}

func (m *Manager) DomainsListPath() string { return m.domainsListPath }
func (m *Manager) IPsListPath() string     { return m.ipsListPath }

func (m *Manager) List() ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureReadyLocked(); err != nil {
		return nil, err
	}
	return m.listUnlocked()
}

func (m *Manager) AddMany(values []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureReadyLocked(); err != nil {
		return err
	}

	existing, err := m.listUnlocked()
	if err != nil {
		return err
	}

	seen := make(map[string]struct{}, len(existing))
	for _, e := range existing {
		seen[e.Value] = struct{}{}
	}

	for _, raw := range values {
		entry, err := normalizeEntry(raw)
		if err != nil {
			continue
		}
		if _, exists := seen[entry.Value]; exists {
			continue
		}
		seen[entry.Value] = struct{}{}
		existing = append(existing, entry)
	}

	return m.replaceAllUnlocked(existing)
}

func (m *Manager) Delete(value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureReadyLocked(); err != nil {
		return err
	}

	normalized := strings.ToLower(strings.TrimSpace(value))
	entries, err := m.listUnlocked()
	if err != nil {
		return err
	}

	filtered := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Value != normalized {
			filtered = append(filtered, e)
		}
	}

	return m.replaceAllUnlocked(filtered)
}

func (m *Manager) ensureReadyLocked() error {
	if m.initialized {
		return m.initErr
	}
	m.initialized = true

	if m.db == nil {
		m.initErr = errors.New("blacklist database is not configured")
		return m.initErr
	}

	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS blacklist_entries (
			value TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			position INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_blacklist_entries_position ON blacklist_entries(position)`,
	} {
		if _, err := m.db.Exec(stmt); err != nil {
			m.initErr = err
			return err
		}
	}

	return nil
}

func (m *Manager) listUnlocked() ([]Entry, error) {
	rows, err := m.db.Query(`
		SELECT value, type, position
		FROM blacklist_entries
		ORDER BY position ASC, value ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]Entry, 0, 32)
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.Value, &e.Type, &e.Position); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}

func (m *Manager) replaceAllUnlocked(entries []Entry) error {
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}

	if _, err := tx.Exec(`DELETE FROM blacklist_entries`); err != nil {
		_ = tx.Rollback()
		return err
	}

	for i, e := range entries {
		if _, err := tx.Exec(`INSERT INTO blacklist_entries (value, type, position) VALUES (?, ?, ?)`, e.Value, e.Type, i); err != nil {
			_ = tx.Rollback()
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	return m.syncRuntimeFiles(entries)
}

func (m *Manager) syncRuntimeFiles(entries []Entry) error {
	if err := os.MkdirAll(filepath.Dir(m.domainsListPath), 0o755); err != nil {
		return err
	}

	var domainLines, ipLines []string
	for _, e := range entries {
		if e.Type == "ip" {
			ipLines = append(ipLines, e.Value)
		} else {
			domainLines = append(domainLines, e.Value)
		}
	}

	writeList := func(path string, lines []string) error {
		content := strings.Join(lines, "\n")
		if content != "" {
			content += "\n"
		}
		return os.WriteFile(path, []byte(content), 0o644)
	}

	if err := writeList(m.domainsListPath, domainLines); err != nil {
		return err
	}
	return writeList(m.ipsListPath, ipLines)
}

func normalizeEntry(raw string) (Entry, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	value = strings.TrimPrefix(value, "https://")
	value = strings.TrimPrefix(value, "http://")
	value = strings.Trim(value, "/")

	if value == "" {
		return Entry{}, errors.New("value is required")
	}

	if isIP(value) {
		return Entry{Value: value, Type: "ip"}, nil
	}

	if !domainPattern.MatchString(value) {
		return Entry{}, errors.New("invalid domain or IP")
	}
	return Entry{Value: value, Type: "domain"}, nil
}

func isIP(value string) bool {
	// Handle CIDR notation.
	if strings.Contains(value, "/") {
		_, _, err := net.ParseCIDR(value)
		return err == nil
	}
	return net.ParseIP(value) != nil
}
