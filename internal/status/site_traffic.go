package status

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"xiomi-router-driver/internal/config"
)

const (
	defaultSiteTrafficSampleInterval = 10 * time.Second
	dnsObserverConfigPath            = "/tmp/dnsmasq.d/vpn_manager_observer.conf"
	dnsObserverLogPath               = "/tmp/dnsmasq-vpn-manager.log"
	siteTrafficConnectionRetention   = 24 * time.Hour
	defaultSiteTrafficPageSize       = 20
	defaultDeviceTrafficPageSize     = 6
	maxTrafficPageSize               = 200
)

type SiteTrafficStat struct {
	Domain     string `json:"domain"`
	Bytes      uint64 `json:"bytes"`
	Packets    uint64 `json:"packets"`
	UpdatedAt  string `json:"updatedAt"`
	LastIP     string `json:"lastIp"`
	ViaTunnel  bool   `json:"viaTunnel"`
	RouteLabel string `json:"routeLabel"`
}

type SiteTrafficResponse struct {
	Sites      []SiteTrafficStat `json:"sites"`
	TotalBytes uint64            `json:"totalBytes"`
	UpdatedAt  string            `json:"updatedAt"`
	Page       int               `json:"page"`
	PageSize   int               `json:"pageSize"`
	Total      int               `json:"total"`
	TotalPages int               `json:"totalPages"`
	SourceIP   string            `json:"sourceIp,omitempty"`
}

type DeviceTrafficSiteStat struct {
	Domain     string `json:"domain"`
	Bytes      uint64 `json:"bytes"`
	Packets    uint64 `json:"packets"`
	UpdatedAt  string `json:"updatedAt"`
	LastIP     string `json:"lastIp"`
	ViaTunnel  bool   `json:"viaTunnel"`
	RouteLabel string `json:"routeLabel"`
}

type DeviceTrafficStat struct {
	SourceIP      string                  `json:"sourceIp"`
	DeviceName    string                  `json:"deviceName"`
	DeviceMAC     string                  `json:"deviceMac"`
	Bytes         uint64                  `json:"bytes"`
	Packets       uint64                  `json:"packets"`
	UpdatedAt     string                  `json:"updatedAt"`
	TunneledBytes uint64                  `json:"tunneledBytes"`
	DirectBytes   uint64                  `json:"directBytes"`
	Sites         []DeviceTrafficSiteStat `json:"sites"`
}

type DeviceTrafficOption struct {
	SourceIP   string `json:"sourceIp"`
	DeviceName string `json:"deviceName"`
	DeviceMAC  string `json:"deviceMac"`
}

type DeviceTrafficResponse struct {
	Devices    []DeviceTrafficStat   `json:"devices"`
	Options    []DeviceTrafficOption `json:"options"`
	TotalBytes uint64                `json:"totalBytes"`
	UpdatedAt  string                `json:"updatedAt"`
	Page       int                   `json:"page"`
	PageSize   int                   `json:"pageSize"`
	Total      int                   `json:"total"`
	TotalPages int                   `json:"totalPages"`
	SourceIP   string                `json:"sourceIp,omitempty"`
}

type pagedSiteTrafficResult struct {
	Stats      []SiteTrafficStat
	TotalCount int
	TotalBytes uint64
	UpdatedAt  string
}

type pagedDeviceTrafficResult struct {
	Devices    []DeviceTrafficStat
	Options    []DeviceTrafficOption
	TotalCount int
	TotalBytes uint64
	UpdatedAt  string
}

type dnsObservation struct {
	Domain string
	IP     string
	At     string
}

type deviceIdentity struct {
	Name string
	MAC  string
}

type siteTrafficConnection struct {
	Key        string
	SourceIP   string
	DeviceName string
	DeviceMAC  string
	Domain     string
	LastIP     string
	Bytes      uint64
	Packets    uint64
	ViaTunnel  bool
	RouteLabel string
}

type siteTrafficStore struct {
	db          *sql.DB
	mu          sync.Mutex
	initialized bool
	initErr     error
}

func newSiteTrafficStore(db *sql.DB) *siteTrafficStore {
	return &siteTrafficStore{db: db}
}

func (s *Service) RunSiteTrafficSampler(ctx context.Context) {
	if s.siteTraffic == nil || s.siteTrafficSampleInterval <= 0 || runtime.GOOS != "linux" {
		return
	}

	if err := s.SampleSiteTraffic(); err != nil {
		log.Printf("site traffic sampler initial sample failed: %v", err)
	}

	ticker := time.NewTicker(s.siteTrafficSampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.SampleSiteTraffic(); err != nil {
				log.Printf("site traffic sample failed: %v", err)
			}
		}
	}
}

func (s *Service) SampleSiteTraffic() error {
	if s.siteTraffic == nil || runtime.GOOS != "linux" {
		return nil
	}

	if err := ensureDNSObserverConfig(); err != nil {
		return err
	}

	if err := s.ingestDNSObservationLog(); err != nil {
		return err
	}

	state, err := s.state.Load()
	if err != nil {
		return err
	}

	observedIPs, err := s.siteTraffic.ObservedIPs()
	if err != nil {
		return err
	}

	deviceIdentities, err := resolveDeviceIdentities()
	if err != nil {
		return err
	}

	entries, err := readConntrackSiteTraffic(state, observedIPs, deviceIdentities)
	if err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	return s.siteTraffic.UpsertConnections(entries, now)
}

func (s *Service) SiteTraffic(scope string, sortBy string, sourceIP string, search string, page int, pageSize int) (SiteTrafficResponse, error) {
	page = normalizeTrafficPage(page)
	pageSize = normalizeTrafficPageSize(pageSize, defaultSiteTrafficPageSize)

	if s.siteTraffic == nil {
		return SiteTrafficResponse{
			Sites:      []SiteTrafficStat{},
			Page:       page,
			PageSize:   pageSize,
			Total:      0,
			TotalPages: 0,
			SourceIP:   strings.TrimSpace(sourceIP),
		}, nil
	}

	result, err := s.siteTraffic.List(scope, sortBy, sourceIP, search, page, pageSize)
	if err != nil {
		return SiteTrafficResponse{}, err
	}

	return SiteTrafficResponse{
		Sites:      result.Stats,
		TotalBytes: result.TotalBytes,
		UpdatedAt:  result.UpdatedAt,
		Page:       page,
		PageSize:   pageSize,
		Total:      result.TotalCount,
		TotalPages: totalTrafficPages(result.TotalCount, pageSize),
		SourceIP:   strings.TrimSpace(sourceIP),
	}, nil
}

func (s *Service) ResetSiteTraffic() error {
	if s.siteTraffic == nil {
		return nil
	}
	return s.siteTraffic.Reset()
}

func (s *Service) DeviceTraffic(scope string, sortBy string, sourceIP string, search string, page int, pageSize int, siteLimit int) (DeviceTrafficResponse, error) {
	page = normalizeTrafficPage(page)
	pageSize = normalizeTrafficPageSize(pageSize, defaultDeviceTrafficPageSize)

	if s.siteTraffic == nil {
		return DeviceTrafficResponse{
			Devices:    []DeviceTrafficStat{},
			Options:    []DeviceTrafficOption{},
			Page:       page,
			PageSize:   pageSize,
			Total:      0,
			TotalPages: 0,
			SourceIP:   strings.TrimSpace(sourceIP),
		}, nil
	}

	result, err := s.siteTraffic.ListDevices(scope, sortBy, sourceIP, search, page, pageSize, siteLimit)
	if err != nil {
		return DeviceTrafficResponse{}, err
	}

	return DeviceTrafficResponse{
		Devices:    result.Devices,
		Options:    result.Options,
		TotalBytes: result.TotalBytes,
		UpdatedAt:  result.UpdatedAt,
		Page:       page,
		PageSize:   pageSize,
		Total:      result.TotalCount,
		TotalPages: totalTrafficPages(result.TotalCount, pageSize),
		SourceIP:   strings.TrimSpace(sourceIP),
	}, nil
}

func (s *Service) ingestDNSObservationLog() error {
	offset, err := s.siteTraffic.LogOffset()
	if err != nil {
		return err
	}

	file, err := os.Open(dnsObserverLogPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open dns observer log: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat dns observer log: %w", err)
	}
	if offset > info.Size() {
		offset = 0
	}

	if _, err := file.Seek(offset, 0); err != nil {
		return fmt.Errorf("seek dns observer log: %w", err)
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 16*1024), 1024*1024)

	observations := make([]dnsObservation, 0, 32)
	now := time.Now().UTC().Format(time.RFC3339)
	for scanner.Scan() {
		domain, ip, ok := parseDNSObservationLine(scanner.Text())
		if !ok {
			continue
		}
		observations = append(observations, dnsObservation{
			Domain: domain,
			IP:     ip,
			At:     now,
		})
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan dns observer log: %w", err)
	}

	currentOffset, err := file.Seek(0, 1)
	if err != nil {
		return fmt.Errorf("read dns observer offset: %w", err)
	}

	if len(observations) > 0 {
		if err := s.siteTraffic.UpsertObservedIPs(observations); err != nil {
			return err
		}
	}

	return s.siteTraffic.SetLogOffset(currentOffset)
}

var dnsReplyPattern = regexp.MustCompile(`\b(?:reply|cached)\s+([^\s]+)\s+is\s+((?:\d{1,3}\.){3}\d{1,3})\b`)

func parseDNSObservationLine(line string) (string, string, bool) {
	match := dnsReplyPattern.FindStringSubmatch(strings.TrimSpace(line))
	if len(match) < 3 {
		return "", "", false
	}

	domain := normalizeObservedDomain(match[1])
	if domain == "" {
		return "", "", false
	}

	ip := strings.TrimSpace(match[2])
	if net.ParseIP(ip) == nil || strings.Contains(ip, ":") {
		return "", "", false
	}

	return domain, ip, true
}

func normalizeObservedDomain(raw string) string {
	value := strings.Trim(strings.ToLower(strings.TrimSpace(raw)), ".")
	switch {
	case value == "":
		return ""
	case strings.HasSuffix(value, ".arpa"):
		return ""
	case strings.HasSuffix(value, ".localdomain"):
		return ""
	default:
		return value
	}
}

func ensureDNSObserverConfig() error {
	if runtime.GOOS != "linux" {
		return nil
	}

	desired := strings.Join([]string{
		"log-queries=extra",
		"log-facility=" + dnsObserverLogPath,
		"",
	}, "\n")

	if err := os.MkdirAll(filepath.Dir(dnsObserverConfigPath), 0o755); err != nil {
		return fmt.Errorf("prepare dns observer config dir: %w", err)
	}

	existing, err := os.ReadFile(dnsObserverConfigPath)
	if err == nil && string(existing) == desired {
		return nil
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read dns observer config: %w", err)
	}

	if err := os.WriteFile(dnsObserverConfigPath, []byte(desired), 0o644); err != nil {
		return fmt.Errorf("write dns observer config: %w", err)
	}

	cmd := exec.Command("/etc/init.d/dnsmasq", "restart")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("restart dnsmasq for observer config: %s", strings.TrimSpace(string(output)))
	}
	return nil
}

func resolveDeviceIdentities() (map[string]deviceIdentity, error) {
	identities := make(map[string]deviceIdentity)

	if err := mergeDHCPLeases(identities); err != nil {
		return nil, err
	}
	if err := mergeARPIdentities(identities); err != nil {
		return nil, err
	}

	return identities, nil
}

func mergeDHCPLeases(identities map[string]deviceIdentity) error {
	file, err := os.Open("/tmp/dhcp.leases")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open dhcp leases: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}

		mac := normalizeMAC(fields[1])
		ip := strings.TrimSpace(fields[2])
		name := strings.TrimSpace(fields[3])
		if ip == "" {
			continue
		}

		current := identities[ip]
		if name != "" && name != "*" {
			current.Name = name
		}
		if mac != "" {
			current.MAC = mac
		}
		identities[ip] = current
	}

	return scanner.Err()
}

func mergeARPIdentities(identities map[string]deviceIdentity) error {
	file, err := os.Open("/proc/net/arp")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open arp table: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	firstLine := true
	for scanner.Scan() {
		if firstLine {
			firstLine = false
			continue
		}

		fields := strings.Fields(scanner.Text())
		if len(fields) < 6 {
			continue
		}

		ip := strings.TrimSpace(fields[0])
		mac := normalizeMAC(fields[3])
		if ip == "" || mac == "" {
			continue
		}

		current := identities[ip]
		if current.MAC == "" {
			current.MAC = mac
		}
		identities[ip] = current
	}

	return scanner.Err()
}

func normalizeMAC(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" || value == "*" || value == "00:00:00:00:00:00" {
		return ""
	}
	return value
}

func readConntrackSiteTraffic(state config.State, observedIPs map[string]string, deviceIdentities map[string]deviceIdentity) ([]siteTrafficConnection, error) {
	lanNetworks, err := lanNetworksForInterface(state.Routing.LANIface)
	if err != nil {
		return nil, err
	}

	file, err := os.Open("/proc/net/nf_conntrack")
	if err != nil {
		return nil, fmt.Errorf("open nf_conntrack: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	entries := make([]siteTrafficConnection, 0, 128)
	for scanner.Scan() {
		entry, ok := parseConntrackLine(scanner.Text())
		if !ok {
			continue
		}
		if !ipInAnySubnet(entry.srcIP, lanNetworks) {
			continue
		}
		if ipInAnySubnet(entry.dstIP, lanNetworks) {
			continue
		}

		domain := normalizeObservedDomain(observedIPs[entry.dstIP])
		if domain == "" {
			domain = entry.dstIP
		}

		viaTunnel, routeLabel := resolveObservedRoute(domain, state)
		identity := deviceIdentities[entry.srcIP]
		entries = append(entries, siteTrafficConnection{
			Key:        entry.key,
			SourceIP:   entry.srcIP,
			DeviceName: strings.TrimSpace(identity.Name),
			DeviceMAC:  strings.TrimSpace(identity.MAC),
			Domain:     domain,
			LastIP:     entry.dstIP,
			Bytes:      entry.bytes,
			Packets:    entry.packets,
			ViaTunnel:  viaTunnel,
			RouteLabel: routeLabel,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan nf_conntrack: %w", err)
	}

	return entries, nil
}

type conntrackLine struct {
	key     string
	srcIP   string
	dstIP   string
	bytes   uint64
	packets uint64
}

func parseConntrackLine(line string) (conntrackLine, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 12 {
		return conntrackLine{}, false
	}

	sequence := make([]string, 0, 12)
	var proto string
	for _, field := range fields {
		if proto == "" && (field == "tcp" || field == "udp" || field == "icmp") {
			proto = field
		}
		if strings.HasPrefix(field, "src=") ||
			strings.HasPrefix(field, "dst=") ||
			strings.HasPrefix(field, "sport=") ||
			strings.HasPrefix(field, "dport=") ||
			strings.HasPrefix(field, "packets=") ||
			strings.HasPrefix(field, "bytes=") {
			sequence = append(sequence, field)
			if len(sequence) == 12 {
				break
			}
		}
	}

	if len(sequence) < 12 {
		return conntrackLine{}, false
	}

	origSrc := strings.TrimPrefix(sequence[0], "src=")
	origDst := strings.TrimPrefix(sequence[1], "dst=")
	origSport := strings.TrimPrefix(sequence[2], "sport=")
	origDport := strings.TrimPrefix(sequence[3], "dport=")
	origPackets, err := strconv.ParseUint(strings.TrimPrefix(sequence[4], "packets="), 10, 64)
	if err != nil {
		return conntrackLine{}, false
	}
	origBytes, err := strconv.ParseUint(strings.TrimPrefix(sequence[5], "bytes="), 10, 64)
	if err != nil {
		return conntrackLine{}, false
	}
	replyPackets, err := strconv.ParseUint(strings.TrimPrefix(sequence[10], "packets="), 10, 64)
	if err != nil {
		return conntrackLine{}, false
	}
	replyBytes, err := strconv.ParseUint(strings.TrimPrefix(sequence[11], "bytes="), 10, 64)
	if err != nil {
		return conntrackLine{}, false
	}

	return conntrackLine{
		key:     strings.Join([]string{proto, origSrc, origDst, origSport, origDport}, "|"),
		srcIP:   origSrc,
		dstIP:   origDst,
		bytes:   origBytes + replyBytes,
		packets: origPackets + replyPackets,
	}, true
}

func lanNetworksForInterface(name string) ([]*net.IPNet, error) {
	iface, err := net.InterfaceByName(strings.TrimSpace(name))
	if err != nil {
		return nil, fmt.Errorf("resolve lan interface %q: %w", name, err)
	}

	addrs, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("list addresses for %s: %w", name, err)
	}

	networks := make([]*net.IPNet, 0, len(addrs))
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet == nil || ipNet.IP == nil || ipNet.IP.To4() == nil {
			continue
		}
		networks = append(networks, ipNet)
	}

	if len(networks) == 0 {
		return nil, fmt.Errorf("no IPv4 networks found on %s", name)
	}

	return networks, nil
}

func ipInAnySubnet(raw string, subnets []*net.IPNet) bool {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil {
		return false
	}
	for _, subnet := range subnets {
		if subnet.Contains(ip) {
			return true
		}
	}
	return false
}

func resolveObservedRoute(domain string, state config.State) (bool, string) {
	providersByID := make(map[string]config.Provider, len(state.Providers))
	for _, provider := range state.Providers {
		providersByID[provider.ID] = provider
	}

	for _, rule := range state.Rules {
		if !rule.Enabled {
			continue
		}
		provider, exists := providersByID[rule.ProviderID]
		if !exists || !provider.Enabled {
			continue
		}
		if !matchesObservedDomain(domain, rule.Domains) {
			continue
		}

		routeLabel := provider.Name
		switch provider.Type {
		case config.ProviderTypeSubscription:
			if location := strings.TrimSpace(rule.SelectedLocation); location != "" {
				routeLabel += " / " + location
			}
		case config.ProviderTypeOpenVPN:
			if iface := strings.TrimSpace(state.Routing.VPNIface); iface != "" {
				routeLabel += " / " + iface
			}
		}
		return true, routeLabel
	}

	return false, ""
}

func matchesObservedDomain(observed string, candidates []string) bool {
	observed = normalizeObservedDomain(observed)
	if observed == "" {
		return false
	}

	for _, candidate := range candidates {
		candidate = normalizeObservedDomain(candidate)
		if candidate == "" {
			continue
		}
		if observed == candidate || strings.HasSuffix(observed, "."+candidate) {
			return true
		}
	}

	return false
}

func resolveSiteTrafficSampleInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("VPN_MANAGER_SITE_TRAFFIC_SAMPLE_INTERVAL"))
	if raw == "" {
		return defaultSiteTrafficSampleInterval
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < 5*time.Second {
		return defaultSiteTrafficSampleInterval
	}

	return parsed
}

func (s *siteTrafficStore) ensureReady() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.initialized {
		return s.initErr
	}
	s.initialized = true

	if s.db == nil {
		s.initErr = errors.New("site traffic database is not configured")
		return s.initErr
	}

	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS site_traffic (
			domain TEXT PRIMARY KEY,
			bytes INTEGER NOT NULL DEFAULT 0,
			packets INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL DEFAULT '',
			last_ip TEXT NOT NULL DEFAULT '',
			via_tunnel INTEGER NOT NULL DEFAULT 0,
			route_label TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS site_traffic_connections (
			conn_key TEXT PRIMARY KEY,
			domain TEXT NOT NULL DEFAULT '',
			bytes INTEGER NOT NULL DEFAULT 0,
			packets INTEGER NOT NULL DEFAULT 0,
			last_seen TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS device_traffic (
			source_ip TEXT PRIMARY KEY,
			device_name TEXT NOT NULL DEFAULT '',
			device_mac TEXT NOT NULL DEFAULT '',
			bytes INTEGER NOT NULL DEFAULT 0,
			packets INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL DEFAULT '',
			tunneled_bytes INTEGER NOT NULL DEFAULT 0,
			direct_bytes INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS device_site_traffic (
			source_ip TEXT NOT NULL,
			device_name TEXT NOT NULL DEFAULT '',
			device_mac TEXT NOT NULL DEFAULT '',
			domain TEXT NOT NULL,
			bytes INTEGER NOT NULL DEFAULT 0,
			packets INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL DEFAULT '',
			last_ip TEXT NOT NULL DEFAULT '',
			via_tunnel INTEGER NOT NULL DEFAULT 0,
			route_label TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (source_ip, domain)
		)`,
		`CREATE TABLE IF NOT EXISTS device_site_traffic_history (
			source_ip TEXT NOT NULL,
			domain TEXT NOT NULL,
			bucket_at TEXT NOT NULL,
			device_name TEXT NOT NULL DEFAULT '',
			device_mac TEXT NOT NULL DEFAULT '',
			bytes INTEGER NOT NULL DEFAULT 0,
			packets INTEGER NOT NULL DEFAULT 0,
			last_ip TEXT NOT NULL DEFAULT '',
			via_tunnel INTEGER NOT NULL DEFAULT 0,
			route_label TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (source_ip, domain, bucket_at)
		)`,
		`CREATE TABLE IF NOT EXISTS device_traffic_history (
			source_ip TEXT NOT NULL,
			bucket_at TEXT NOT NULL,
			device_name TEXT NOT NULL DEFAULT '',
			device_mac TEXT NOT NULL DEFAULT '',
			bytes INTEGER NOT NULL DEFAULT 0,
			packets INTEGER NOT NULL DEFAULT 0,
			tunneled_bytes INTEGER NOT NULL DEFAULT 0,
			direct_bytes INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (source_ip, bucket_at)
		)`,
		`CREATE TABLE IF NOT EXISTS site_dns_observations (
			ip TEXT PRIMARY KEY,
			domain TEXT NOT NULL,
			observed_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS site_traffic_meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			s.initErr = err
			return err
		}
	}

	return nil
}

func (s *siteTrafficStore) UpsertObservedIPs(observations []dnsObservation) error {
	if err := s.ensureReady(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	for _, item := range observations {
		if _, err := tx.Exec(`
			INSERT INTO site_dns_observations (ip, domain, observed_at)
			VALUES (?, ?, ?)
			ON CONFLICT(ip) DO UPDATE SET
				domain = excluded.domain,
				observed_at = excluded.observed_at
		`, item.IP, item.Domain, item.At); err != nil {
			_ = tx.Rollback()
			return err
		}
	}

	return tx.Commit()
}

func (s *siteTrafficStore) ObservedIPs() (map[string]string, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`SELECT ip, domain FROM site_dns_observations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var ip, domain string
		if err := rows.Scan(&ip, &domain); err != nil {
			return nil, err
		}
		out[ip] = domain
	}

	return out, rows.Err()
}

func (s *siteTrafficStore) LogOffset() (int64, error) {
	if err := s.ensureReady(); err != nil {
		return 0, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var raw string
	err := s.db.QueryRow(`SELECT value FROM site_traffic_meta WHERE key = 'dns_log_offset'`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return 0, nil
	}
	return value, nil
}

func (s *siteTrafficStore) SetLogOffset(offset int64) error {
	if err := s.ensureReady(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO site_traffic_meta (key, value)
		VALUES ('dns_log_offset', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, strconv.FormatInt(offset, 10))
	return err
}

func (s *siteTrafficStore) UpsertConnections(entries []siteTrafficConnection, now string) error {
	if err := s.ensureReady(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	bucketAt := deviceTrafficHistoryBucketAt(now)

	for _, entry := range entries {
		var prevBytes, prevPackets uint64
		_ = tx.QueryRow(`
			SELECT bytes, packets
			FROM site_traffic_connections
			WHERE conn_key = ?
		`, entry.Key).Scan(&prevBytes, &prevPackets)

		deltaBytes := counterDelta(entry.Bytes, prevBytes)
		deltaPackets := counterDelta(entry.Packets, prevPackets)

		if _, err := tx.Exec(`
			INSERT INTO site_traffic_connections (conn_key, domain, bytes, packets, last_seen)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(conn_key) DO UPDATE SET
				domain = excluded.domain,
				bytes = excluded.bytes,
				packets = excluded.packets,
				last_seen = excluded.last_seen
		`, entry.Key, entry.Domain, entry.Bytes, entry.Packets, now); err != nil {
			_ = tx.Rollback()
			return err
		}

		if deltaBytes == 0 && deltaPackets == 0 {
			continue
		}

		if _, err := tx.Exec(`
			INSERT INTO site_traffic (domain, bytes, packets, updated_at, last_ip, via_tunnel, route_label)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(domain) DO UPDATE SET
				bytes = site_traffic.bytes + excluded.bytes,
				packets = site_traffic.packets + excluded.packets,
				updated_at = excluded.updated_at,
				last_ip = excluded.last_ip,
				via_tunnel = excluded.via_tunnel,
				route_label = excluded.route_label
		`, entry.Domain, deltaBytes, deltaPackets, now, entry.LastIP, boolToInt(entry.ViaTunnel), entry.RouteLabel); err != nil {
			_ = tx.Rollback()
			return err
		}

		tunneledBytes := uint64(0)
		directBytes := deltaBytes
		if entry.ViaTunnel {
			tunneledBytes = deltaBytes
			directBytes = 0
		}

		if _, err := tx.Exec(`
			INSERT INTO device_traffic (source_ip, device_name, device_mac, bytes, packets, updated_at, tunneled_bytes, direct_bytes)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(source_ip) DO UPDATE SET
				device_name = CASE WHEN excluded.device_name <> '' THEN excluded.device_name ELSE device_traffic.device_name END,
				device_mac = CASE WHEN excluded.device_mac <> '' THEN excluded.device_mac ELSE device_traffic.device_mac END,
				bytes = device_traffic.bytes + excluded.bytes,
				packets = device_traffic.packets + excluded.packets,
				updated_at = excluded.updated_at,
				tunneled_bytes = device_traffic.tunneled_bytes + excluded.tunneled_bytes,
				direct_bytes = device_traffic.direct_bytes + excluded.direct_bytes
		`, entry.SourceIP, entry.DeviceName, entry.DeviceMAC, deltaBytes, deltaPackets, now, tunneledBytes, directBytes); err != nil {
			_ = tx.Rollback()
			return err
		}

		if _, err := tx.Exec(`
			INSERT INTO device_site_traffic (source_ip, device_name, device_mac, domain, bytes, packets, updated_at, last_ip, via_tunnel, route_label)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(source_ip, domain) DO UPDATE SET
				device_name = CASE WHEN excluded.device_name <> '' THEN excluded.device_name ELSE device_site_traffic.device_name END,
				device_mac = CASE WHEN excluded.device_mac <> '' THEN excluded.device_mac ELSE device_site_traffic.device_mac END,
				bytes = device_site_traffic.bytes + excluded.bytes,
				packets = device_site_traffic.packets + excluded.packets,
				updated_at = excluded.updated_at,
				last_ip = excluded.last_ip,
				via_tunnel = excluded.via_tunnel,
				route_label = excluded.route_label
		`, entry.SourceIP, entry.DeviceName, entry.DeviceMAC, entry.Domain, deltaBytes, deltaPackets, now, entry.LastIP, boolToInt(entry.ViaTunnel), entry.RouteLabel); err != nil {
			_ = tx.Rollback()
			return err
		}

		if _, err := tx.Exec(`
			INSERT INTO device_traffic_history (source_ip, bucket_at, device_name, device_mac, bytes, packets, tunneled_bytes, direct_bytes)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(source_ip, bucket_at) DO UPDATE SET
				device_name = CASE WHEN excluded.device_name <> '' THEN excluded.device_name ELSE device_traffic_history.device_name END,
				device_mac = CASE WHEN excluded.device_mac <> '' THEN excluded.device_mac ELSE device_traffic_history.device_mac END,
				bytes = device_traffic_history.bytes + excluded.bytes,
				packets = device_traffic_history.packets + excluded.packets,
				tunneled_bytes = device_traffic_history.tunneled_bytes + excluded.tunneled_bytes,
				direct_bytes = device_traffic_history.direct_bytes + excluded.direct_bytes
		`, entry.SourceIP, bucketAt, entry.DeviceName, entry.DeviceMAC, deltaBytes, deltaPackets, tunneledBytes, directBytes); err != nil {
			_ = tx.Rollback()
			return err
		}

		if _, err := tx.Exec(`
			INSERT INTO device_site_traffic_history (source_ip, domain, bucket_at, device_name, device_mac, bytes, packets, last_ip, via_tunnel, route_label)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(source_ip, domain, bucket_at) DO UPDATE SET
				device_name = CASE WHEN excluded.device_name <> '' THEN excluded.device_name ELSE device_site_traffic_history.device_name END,
				device_mac = CASE WHEN excluded.device_mac <> '' THEN excluded.device_mac ELSE device_site_traffic_history.device_mac END,
				bytes = device_site_traffic_history.bytes + excluded.bytes,
				packets = device_site_traffic_history.packets + excluded.packets,
				last_ip = excluded.last_ip,
				via_tunnel = excluded.via_tunnel,
				route_label = excluded.route_label
		`, entry.SourceIP, entry.Domain, bucketAt, entry.DeviceName, entry.DeviceMAC, deltaBytes, deltaPackets, entry.LastIP, boolToInt(entry.ViaTunnel), entry.RouteLabel); err != nil {
			_ = tx.Rollback()
			return err
		}
	}

	cutoff := time.Now().UTC().Add(-siteTrafficConnectionRetention).Format(time.RFC3339)
	if _, err := tx.Exec(`DELETE FROM site_traffic_connections WHERE last_seen < ?`, cutoff); err != nil {
		_ = tx.Rollback()
		return err
	}
	historyCutoff := time.Now().UTC().Add(-trafficHistoryRetention).Format(time.RFC3339)
	if _, err := tx.Exec(`DELETE FROM device_traffic_history WHERE bucket_at < ?`, historyCutoff); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec(`DELETE FROM device_site_traffic_history WHERE bucket_at < ?`, historyCutoff); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

func (s *siteTrafficStore) List(scope string, sortBy string, sourceIP string, search string, page int, pageSize int) (pagedSiteTrafficResult, error) {
	if err := s.ensureReady(); err != nil {
		return pagedSiteTrafficResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tableName := "site_traffic"
	conditions := make([]string, 0, 4)
	args := make([]any, 0, 4)

	if sourceIP = strings.TrimSpace(sourceIP); sourceIP != "" {
		tableName = "device_site_traffic"
		conditions = append(conditions, "source_ip = ?")
		args = append(args, sourceIP)
	}

	switch strings.TrimSpace(scope) {
	case "tunneled":
		conditions = append(conditions, "via_tunnel = 1")
	case "direct":
		conditions = append(conditions, "via_tunnel = 0")
	}

	if query := strings.ToLower(strings.TrimSpace(search)); query != "" {
		like := "%" + query + "%"
		conditions = append(conditions, "(LOWER(domain) LIKE ? OR last_ip LIKE ?)")
		args = append(args, like, like)
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	var totalCount int
	var totalBytes sql.NullInt64
	var updatedAt sql.NullString
	if err := s.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(bytes), 0), COALESCE(MAX(updated_at), '') FROM `+tableName+where,
		args...,
	).Scan(&totalCount, &totalBytes, &updatedAt); err != nil {
		return pagedSiteTrafficResult{}, err
	}

	query := `SELECT domain, bytes, packets, updated_at, last_ip, via_tunnel, route_label FROM ` + tableName + where +
		` ORDER BY ` + siteTrafficOrderClause(sortBy) + ` LIMIT ? OFFSET ?`
	queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)

	rows, err := s.db.Query(query, queryArgs...)
	if err != nil {
		return pagedSiteTrafficResult{}, err
	}
	defer rows.Close()

	stats := make([]SiteTrafficStat, 0, 64)
	for rows.Next() {
		var item SiteTrafficStat
		var viaTunnel int
		if err := rows.Scan(&item.Domain, &item.Bytes, &item.Packets, &item.UpdatedAt, &item.LastIP, &viaTunnel, &item.RouteLabel); err != nil {
			return pagedSiteTrafficResult{}, err
		}
		item.ViaTunnel = viaTunnel == 1
		stats = append(stats, item)
	}
	if err := rows.Err(); err != nil {
		return pagedSiteTrafficResult{}, err
	}

	return pagedSiteTrafficResult{
		Stats:      stats,
		TotalCount: totalCount,
		TotalBytes: nullInt64ToUint64(totalBytes),
		UpdatedAt:  updatedAt.String,
	}, nil
}

func (s *siteTrafficStore) ListDevices(scope string, sortBy string, sourceIP string, search string, page int, pageSize int, siteLimit int) (pagedDeviceTrafficResult, error) {
	if err := s.ensureReady(); err != nil {
		return pagedDeviceTrafficResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	conditions := make([]string, 0, 4)
	args := make([]any, 0, 4)

	if sourceIP = strings.TrimSpace(sourceIP); sourceIP != "" {
		conditions = append(conditions, "source_ip = ?")
		args = append(args, sourceIP)
	}

	switch strings.TrimSpace(scope) {
	case "tunneled":
		conditions = append(conditions, "tunneled_bytes > 0")
	case "direct":
		conditions = append(conditions, "direct_bytes > 0")
	}

	if query := strings.ToLower(strings.TrimSpace(search)); query != "" {
		like := "%" + query + "%"
		conditions = append(conditions, `(
			LOWER(device_name) LIKE ? OR
			source_ip LIKE ? OR
			LOWER(device_mac) LIKE ? OR
			EXISTS (
				SELECT 1
				FROM device_site_traffic dst
				WHERE dst.source_ip = device_traffic.source_ip
				  AND LOWER(dst.domain) LIKE ?
			)
		)`)
		args = append(args, like, like, like, like)
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	var totalCount int
	var totalBytes sql.NullInt64
	var updatedAt sql.NullString
	if err := s.db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(bytes), 0), COALESCE(MAX(updated_at), '') FROM device_traffic`+where,
		args...,
	).Scan(&totalCount, &totalBytes, &updatedAt); err != nil {
		return pagedDeviceTrafficResult{}, err
	}

	options, err := s.listDeviceOptions(scope)
	if err != nil {
		return pagedDeviceTrafficResult{}, err
	}

	query := `SELECT source_ip, device_name, device_mac, bytes, packets, updated_at, tunneled_bytes, direct_bytes FROM device_traffic` +
		where + ` ORDER BY ` + deviceTrafficOrderClause(sortBy) + ` LIMIT ? OFFSET ?`
	queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)

	rows, err := s.db.Query(query, queryArgs...)
	if err != nil {
		return pagedDeviceTrafficResult{}, err
	}
	defer rows.Close()

	devices := make([]DeviceTrafficStat, 0, 32)
	for rows.Next() {
		var item DeviceTrafficStat
		if err := rows.Scan(
			&item.SourceIP,
			&item.DeviceName,
			&item.DeviceMAC,
			&item.Bytes,
			&item.Packets,
			&item.UpdatedAt,
			&item.TunneledBytes,
			&item.DirectBytes,
		); err != nil {
			return pagedDeviceTrafficResult{}, err
		}
		devices = append(devices, item)
	}
	if err := rows.Err(); err != nil {
		return pagedDeviceTrafficResult{}, err
	}

	for i := range devices {
		sitesQuery := `SELECT domain, bytes, packets, updated_at, last_ip, via_tunnel, route_label FROM device_site_traffic WHERE source_ip = ?`
		switch strings.TrimSpace(scope) {
		case "tunneled":
			sitesQuery += ` AND via_tunnel = 1`
		case "direct":
			sitesQuery += ` AND via_tunnel = 0`
		}
		sitesQuery += ` ORDER BY bytes DESC`
		siteArgs := []any{devices[i].SourceIP}
		if siteLimit > 0 {
			sitesQuery += ` LIMIT ?`
			siteArgs = append(siteArgs, siteLimit)
		}

		siteRows, err := s.db.Query(sitesQuery, siteArgs...)
		if err != nil {
			return pagedDeviceTrafficResult{}, err
		}

		sites := make([]DeviceTrafficSiteStat, 0, 8)
		for siteRows.Next() {
			var site DeviceTrafficSiteStat
			var viaTunnel int
			if err := siteRows.Scan(&site.Domain, &site.Bytes, &site.Packets, &site.UpdatedAt, &site.LastIP, &viaTunnel, &site.RouteLabel); err != nil {
				siteRows.Close()
				return pagedDeviceTrafficResult{}, err
			}
			site.ViaTunnel = viaTunnel == 1
			sites = append(sites, site)
		}
		siteRows.Close()
		devices[i].Sites = sites
	}

	return pagedDeviceTrafficResult{
		Devices:    devices,
		Options:    options,
		TotalCount: totalCount,
		TotalBytes: nullInt64ToUint64(totalBytes),
		UpdatedAt:  updatedAt.String,
	}, nil
}

func (s *siteTrafficStore) Reset() error {
	if err := s.ensureReady(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	for _, stmt := range []string{
		`DELETE FROM site_traffic`,
		`DELETE FROM site_traffic_connections`,
		`DELETE FROM device_traffic`,
		`DELETE FROM device_site_traffic`,
		`DELETE FROM device_traffic_history`,
		`DELETE FROM device_site_traffic_history`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			return err
		}
	}

	return tx.Commit()
}

func (s *siteTrafficStore) listDeviceOptions(scope string) ([]DeviceTrafficOption, error) {
	query := `SELECT source_ip, device_name, device_mac FROM device_traffic`
	switch strings.TrimSpace(scope) {
	case "tunneled":
		query += ` WHERE tunneled_bytes > 0`
	case "direct":
		query += ` WHERE direct_bytes > 0`
	}
	query += ` ORDER BY LOWER(CASE WHEN TRIM(device_name) <> '' THEN device_name ELSE source_ip END), source_ip`

	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	options := make([]DeviceTrafficOption, 0, 32)
	for rows.Next() {
		var item DeviceTrafficOption
		if err := rows.Scan(&item.SourceIP, &item.DeviceName, &item.DeviceMAC); err != nil {
			return nil, err
		}
		options = append(options, item)
	}

	return options, rows.Err()
}

func siteTrafficOrderClause(sortBy string) string {
	switch strings.TrimSpace(sortBy) {
	case "domain":
		return "LOWER(domain) ASC, bytes DESC"
	case "packets":
		return "packets DESC, bytes DESC, LOWER(domain) ASC"
	default:
		return "bytes DESC, packets DESC, LOWER(domain) ASC"
	}
}

func deviceTrafficOrderClause(sortBy string) string {
	switch strings.TrimSpace(sortBy) {
	case "name":
		return "LOWER(CASE WHEN TRIM(device_name) <> '' THEN device_name ELSE source_ip END) ASC, source_ip ASC"
	case "packets":
		return "packets DESC, bytes DESC, source_ip ASC"
	default:
		return "bytes DESC, packets DESC, source_ip ASC"
	}
}

func normalizeTrafficPage(value int) int {
	if value < 1 {
		return 1
	}
	return value
}

func normalizeTrafficPageSize(value int, fallback int) int {
	if value <= 0 {
		value = fallback
	}
	if value > maxTrafficPageSize {
		return maxTrafficPageSize
	}
	return value
}

func totalTrafficPages(total int, pageSize int) int {
	if total <= 0 || pageSize <= 0 {
		return 0
	}
	return (total + pageSize - 1) / pageSize
}

func nullInt64ToUint64(value sql.NullInt64) uint64 {
	if !value.Valid || value.Int64 <= 0 {
		return 0
	}
	return uint64(value.Int64)
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
