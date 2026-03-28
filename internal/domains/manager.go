package domains

import (
	"bufio"
	"database/sql"
	"errors"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

var domainPattern = regexp.MustCompile(`^[a-z0-9.-]+$`)

type Manager struct {
	db          *sql.DB
	runtimePath string
	legacyPath  string
	mu          sync.Mutex
	initialized bool
	initErr     error
}

func NewManager(db *sql.DB, runtimePath string, legacyPath string) *Manager {
	return &Manager{
		db:          db,
		runtimePath: strings.TrimSpace(runtimePath),
		legacyPath:  strings.TrimSpace(legacyPath),
	}
}

func (m *Manager) RuntimePath() string {
	return m.runtimePath
}

func (m *Manager) List() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureReadyLocked(); err != nil {
		return nil, err
	}

	return m.listUnlocked()
}

func (m *Manager) Add(domain string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureReadyLocked(); err != nil {
		return err
	}

	normalized, err := normalizeDomain(domain)
	if err != nil {
		return err
	}

	domains, err := m.listUnlocked()
	if err != nil {
		return err
	}
	for _, existing := range domains {
		if existing == normalized {
			return nil
		}
	}

	domains = append(domains, normalized)
	return m.replaceAllUnlocked(domains)
}

func (m *Manager) ReplaceAll(domains []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureReadyLocked(); err != nil {
		return err
	}

	return m.replaceAllUnlocked(domains)
}

func (m *Manager) Delete(domain string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureReadyLocked(); err != nil {
		return err
	}

	normalized, err := normalizeDomain(domain)
	if err != nil {
		return err
	}

	domains, err := m.listUnlocked()
	if err != nil {
		return err
	}

	filtered := make([]string, 0, len(domains))
	for _, existing := range domains {
		if existing == normalized {
			continue
		}
		filtered = append(filtered, existing)
	}

	return m.replaceAllUnlocked(filtered)
}

func (m *Manager) ensureReadyLocked() error {
	if m.initialized {
		return m.initErr
	}
	m.initialized = true

	if m.db == nil {
		m.initErr = errors.New("domains database is not configured")
		return m.initErr
	}

	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS current_domains (
			domain TEXT PRIMARY KEY,
			position INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_current_domains_position ON current_domains(position)`,
	} {
		if _, err := m.db.Exec(stmt); err != nil {
			m.initErr = err
			return err
		}
	}

	if err := m.migrateLegacyLocked(); err != nil {
		m.initErr = err
		return err
	}

	return nil
}

func (m *Manager) migrateLegacyLocked() error {
	var count int
	if err := m.db.QueryRow(`SELECT COUNT(1) FROM current_domains`).Scan(&count); err != nil || count > 0 {
		return err
	}

	domains, err := readLegacyDomains(m.legacyPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	return m.replaceAllUnlocked(domains)
}

func (m *Manager) listUnlocked() ([]string, error) {
	rows, err := m.db.Query(`
		SELECT domain
		FROM current_domains
		ORDER BY position ASC, domain ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]string, 0, 32)
	for rows.Next() {
		var domain string
		if err := rows.Scan(&domain); err != nil {
			return nil, err
		}
		result = append(result, domain)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

func (m *Manager) replaceAllUnlocked(domains []string) error {
	normalized, err := normalizeDomainList(domains)
	if err != nil {
		return err
	}

	tx, err := m.db.Begin()
	if err != nil {
		return err
	}

	if _, err := tx.Exec(`DELETE FROM current_domains`); err != nil {
		_ = tx.Rollback()
		return err
	}

	for index, domain := range normalized {
		if _, err := tx.Exec(`
			INSERT INTO current_domains (domain, position)
			VALUES (?, ?)
		`, domain, index); err != nil {
			_ = tx.Rollback()
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	return m.syncRuntimeFile(normalized)
}

func (m *Manager) syncRuntimeFile(lines []string) error {
	if strings.TrimSpace(m.runtimePath) == "" {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(m.runtimePath), 0o755); err != nil {
		return err
	}

	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	return os.WriteFile(m.runtimePath, []byte(content), 0o644)
}

func normalizeDomainList(domains []string) ([]string, error) {
	result := make([]string, 0, len(domains))
	seen := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		normalized, err := normalizeDomain(domain)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result, nil
}

func NormalizeEntries(domains []string) []string {
	result := make([]string, 0, len(domains))
	seen := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		normalized, err := normalizeDomain(domain)
		if err != nil {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result
}

func SplitInput(raw string) []string {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	items := make([]string, 0, len(lines))
	for _, line := range lines {
		withoutComment, _, _ := strings.Cut(line, "#")
		fields := strings.FieldsFunc(withoutComment, func(r rune) bool {
			switch r {
			case ',', ';', '\n', '\r', '\t', ' ':
				return true
			default:
				return false
			}
		})
		items = append(items, fields...)
	}
	return NormalizeEntries(items)
}

func normalizeDomain(domain string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(domain))
	normalized, _, _ = strings.Cut(normalized, "#")
	normalized = strings.TrimSpace(normalized)
	normalized = strings.TrimPrefix(normalized, "https://")
	normalized = strings.TrimPrefix(normalized, "http://")
	if at := strings.LastIndex(normalized, "@"); at >= 0 {
		normalized = normalized[at+1:]
	}
	if slash := strings.Index(normalized, "/"); slash >= 0 {
		normalized = normalized[:slash]
	}
	if host, port, err := net.SplitHostPort(normalized); err == nil && port != "" {
		normalized = host
	} else if host, port, ok := strings.Cut(normalized, ":"); ok && port != "" && strings.IndexByte(port, '.') < 0 {
		if _, err := strconv.Atoi(port); err == nil {
			normalized = host
		}
	}
	normalized = strings.TrimSpace(strings.Trim(normalized, "/"))

	if normalized == "" {
		return "", errors.New("domain is required")
	}

	labels := strings.Split(normalized, ".")
	filtered := make([]string, 0, len(labels))
	for _, label := range labels {
		label = strings.TrimSpace(label)
		label = strings.Trim(label, ".")
		if label == "" {
			continue
		}
		if strings.Contains(label, "*") {
			continue
		}
		filtered = append(filtered, label)
	}
	normalized = strings.Join(filtered, ".")
	normalized = strings.Trim(normalized, ".")

	if normalized == "" {
		return "", errors.New("domain is required")
	}
	if !domainPattern.MatchString(normalized) {
		return "", errors.New("domain may contain only letters, numbers, dots and hyphens")
	}
	if strings.HasPrefix(normalized, ".") || strings.HasSuffix(normalized, ".") {
		return "", errors.New("domain cannot start or end with a dot")
	}
	if strings.Contains(normalized, "..") {
		return "", errors.New("domain cannot contain empty labels")
	}

	labelParts := strings.Split(normalized, ".")
	for _, label := range labelParts {
		if label == "" {
			return "", errors.New("domain cannot contain empty labels")
		}
	}

	for _, label := range labelParts {
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", errors.New("domain labels cannot start or end with a hyphen")
		}
	}
	if len(labelParts) == 0 {
		return "", errors.New("domain cannot contain empty labels")
	}

	return normalized, nil
}

func parseDomainLine(line string) (string, bool) {
	trimmed, _, _ := strings.Cut(line, "#")
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", false
	}

	normalized, err := normalizeDomain(trimmed)
	if err != nil {
		return "", false
	}
	return normalized, true
}

func readLegacyDomains(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lines := make([]string, 0, 64)
	for scanner.Scan() {
		if domain, ok := parseDomainLine(scanner.Text()); ok {
			lines = append(lines, domain)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return normalizeDomainList(lines)
}
