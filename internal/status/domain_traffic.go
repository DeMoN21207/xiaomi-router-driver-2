package status

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"xiomi-router-driver/internal/routing"
)

// DomainTrafficStat represents accumulated traffic for a single domain.
type DomainTrafficStat struct {
	Domain    string `json:"domain"`
	RXBytes   uint64 `json:"rxBytes"`
	TXBytes   uint64 `json:"txBytes"`
	Bytes     uint64 `json:"bytes"`
	Packets   uint64 `json:"packets"`
	UpdatedAt string `json:"updatedAt"`
}

// DomainTrafficResponse is the API response for /api/traffic/domains.
type DomainTrafficResponse struct {
	Domains    []DomainTrafficStat `json:"domains"`
	TotalBytes uint64              `json:"totalBytes"`
	UpdatedAt  string              `json:"updatedAt"`
}

type domainTrafficStore struct {
	db          *sql.DB
	mu          sync.Mutex
	initialized bool
	initErr     error
}

func newDomainTrafficStore(db *sql.DB) *domainTrafficStore {
	return &domainTrafficStore{db: db}
}

// readIptablesChainCounters parses `iptables -L <chain> -v -n -x` output
// and extracts per-domain byte/packet counters from rule comments.
//
// Example output line:
//
//	1234  56789  all  --  *  *  0.0.0.0/0  0.0.0.0/0  match-set vpn_d_vpn_hosts_a1b2c3d4 dst /* example.com */
func readIptablesChainCounters(chainName string) ([]DomainTrafficStat, error) {
	cmd := exec.Command("iptables", "-L", chainName, "-v", "-n", "-x")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("iptables -L %s: %s", chainName, strings.TrimSpace(string(output)))
	}

	return parseIptablesOutput(string(output))
}

var commentRegex = regexp.MustCompile(`/\*\s*(.+?)\s*\*/`)

func parseIptablesOutput(output string) ([]DomainTrafficStat, error) {
	var stats []DomainTrafficStat
	lines := strings.Split(output, "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "Chain ") || strings.HasPrefix(line, "pkts") {
			continue
		}

		// Extract comment (domain name)
		match := commentRegex.FindStringSubmatch(line)
		if len(match) < 2 {
			continue
		}
		domain := strings.TrimSpace(match[1])

		// Parse pkts and bytes from the beginning of the line
		// Format: pkts bytes target prot opt in out source destination ...
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		packets, _ := strconv.ParseUint(fields[0], 10, 64)
		bytes, _ := strconv.ParseUint(fields[1], 10, 64)

		stats = append(stats, DomainTrafficStat{
			Domain:  domain,
			Bytes:   bytes,
			Packets: packets,
		})
	}

	return stats, nil
}

// SampleDomainTraffic reads current iptables counters and stores deltas in the DB.
func (s *Service) SampleDomainTraffic() error {
	if s.domainTraffic == nil {
		return nil
	}

	// Build chain names from active routing settings
	chains := s.activeDomainStatsChains()
	if len(chains) == 0 {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, chain := range chains {
		stats, err := readIptablesChainCounters(chain)
		if err != nil {
			log.Printf("domain traffic: skip chain %s: %v", chain, err)
			continue
		}

		if err := s.domainTraffic.Upsert(stats, now); err != nil {
			return fmt.Errorf("domain traffic upsert: %w", err)
		}
	}

	return nil
}

func (s *Service) activeDomainStatsChains() []string {
	state, err := s.state.Load()
	if err != nil {
		return nil
	}

	seen := make(map[string]bool)
	var chains []string

	// Main routing ipset -> chain
	if s.domains != nil {
		domainCount, countErr := s.domains.CountDomains()
		if countErr == nil && routing.DomainStatsEnabled(domainCount) {
			mainChain := routing.DomainStatsChainName(state.Routing.IPSetName)
			if !seen[mainChain] {
				seen[mainChain] = true
				chains = append(chains, mainChain)
			}
		}
	}

	// Subscription instances have their own ipset names
	if s.subscriptions != nil {
		snapshots, err := s.subscriptions.Snapshots()
		if err == nil {
			for _, snap := range snapshots {
				if snap.IPSetName != "" && routing.DomainStatsEnabled(snap.DomainCount) {
					chain := routing.DomainStatsChainName(snap.IPSetName)
					if !seen[chain] {
						seen[chain] = true
						chains = append(chains, chain)
					}
				}
			}
		}
	}

	return chains
}

// DomainTraffic returns aggregated per-domain traffic stats.
func (s *Service) DomainTraffic(sortBy string, limit int) (DomainTrafficResponse, error) {
	if s.domainTraffic == nil {
		return DomainTrafficResponse{Domains: []DomainTrafficStat{}}, nil
	}
	if len(s.activeDomainStatsChains()) == 0 {
		return DomainTrafficResponse{Domains: []DomainTrafficStat{}}, nil
	}

	result, err := s.domainTraffic.List(sortBy, limit)
	if err != nil {
		return DomainTrafficResponse{}, err
	}

	return DomainTrafficResponse{
		Domains:    result.Stats,
		TotalBytes: result.TotalBytes,
		UpdatedAt:  result.UpdatedAt,
	}, nil
}

// ResetDomainTraffic clears all accumulated domain traffic stats.
func (s *Service) ResetDomainTraffic() error {
	if s.domainTraffic == nil {
		return nil
	}
	return s.domainTraffic.Reset()
}

// --- Store ---

func (s *domainTrafficStore) ensureReady() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.initialized {
		return s.initErr
	}
	s.initialized = true

	if s.db == nil {
		s.initErr = errors.New("domain traffic database is not configured")
		return s.initErr
	}

	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS domain_traffic (
			domain TEXT PRIMARY KEY,
			bytes INTEGER NOT NULL DEFAULT 0,
			packets INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS domain_traffic_counters (
			domain TEXT PRIMARY KEY,
			bytes INTEGER NOT NULL DEFAULT 0,
			packets INTEGER NOT NULL DEFAULT 0
		)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			s.initErr = err
			return err
		}
	}

	return nil
}

func (s *domainTrafficStore) Upsert(stats []DomainTrafficStat, now string) error {
	if err := s.ensureReady(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	for _, stat := range stats {
		if stat.Bytes == 0 && stat.Packets == 0 {
			continue
		}

		// Get previous counter value to compute delta
		var prevBytes, prevPackets uint64
		_ = tx.QueryRow(`SELECT bytes, packets FROM domain_traffic_counters WHERE domain = ?`, stat.Domain).Scan(&prevBytes, &prevPackets)

		// Compute delta (handle counter reset)
		deltaBytes := counterDelta(stat.Bytes, prevBytes)
		deltaPackets := counterDelta(stat.Packets, prevPackets)

		// Update raw counter snapshot
		if _, err := tx.Exec(`
			INSERT INTO domain_traffic_counters (domain, bytes, packets)
			VALUES (?, ?, ?)
			ON CONFLICT(domain) DO UPDATE SET bytes = excluded.bytes, packets = excluded.packets
		`, stat.Domain, stat.Bytes, stat.Packets); err != nil {
			_ = tx.Rollback()
			return err
		}

		// Accumulate deltas
		if deltaBytes > 0 || deltaPackets > 0 {
			if _, err := tx.Exec(`
				INSERT INTO domain_traffic (domain, bytes, packets, updated_at)
				VALUES (?, ?, ?, ?)
				ON CONFLICT(domain) DO UPDATE SET
					bytes = domain_traffic.bytes + excluded.bytes,
					packets = domain_traffic.packets + excluded.packets,
					updated_at = excluded.updated_at
			`, stat.Domain, deltaBytes, deltaPackets, now); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
	}

	return tx.Commit()
}

type domainTrafficQueryResult struct {
	Stats      []DomainTrafficStat
	TotalBytes uint64
	UpdatedAt  string
}

func (s *domainTrafficStore) List(sortBy string, limit int) (domainTrafficQueryResult, error) {
	if err := s.ensureReady(); err != nil {
		return domainTrafficQueryResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var totalBytes sql.NullInt64
	var updatedAt sql.NullString
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(bytes), 0), COALESCE(MAX(updated_at), '') FROM domain_traffic`).Scan(&totalBytes, &updatedAt); err != nil {
		return domainTrafficQueryResult{}, err
	}

	query := `SELECT domain, bytes, packets, updated_at FROM domain_traffic ORDER BY ` + domainTrafficOrderClause(sortBy)
	var (
		rows *sql.Rows
		err  error
	)
	if limit > 0 {
		query += ` LIMIT ?`
		rows, err = s.db.Query(query, limit)
	} else {
		rows, err = s.db.Query(query)
	}
	if err != nil {
		return domainTrafficQueryResult{}, err
	}
	defer rows.Close()

	stats := make([]DomainTrafficStat, 0, 32)
	for rows.Next() {
		var stat DomainTrafficStat
		if err := rows.Scan(&stat.Domain, &stat.Bytes, &stat.Packets, &stat.UpdatedAt); err != nil {
			return domainTrafficQueryResult{}, err
		}
		stats = append(stats, stat)
	}
	if err := rows.Err(); err != nil {
		return domainTrafficQueryResult{}, err
	}

	return domainTrafficQueryResult{
		Stats:      stats,
		TotalBytes: nullInt64ToUint64(totalBytes),
		UpdatedAt:  updatedAt.String,
	}, nil
}

func (s *domainTrafficStore) Reset() error {
	if err := s.ensureReady(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.db.Exec(`DELETE FROM domain_traffic`); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM domain_traffic_counters`)
	return err
}

func domainTrafficOrderClause(sortBy string) string {
	switch strings.TrimSpace(sortBy) {
	case "domain":
		return `domain ASC`
	case "packets":
		return `packets DESC, domain ASC`
	default:
		return `bytes DESC, domain ASC`
	}
}
