package status

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/domains"
	"xiomi-router-driver/internal/openvpn"
	"xiomi-router-driver/internal/subscription"
)

const (
	defaultDomainHealthSampleInterval = 12 * time.Hour
	domainHealthInitialDelay          = 15 * time.Second
	domainHealthDNSCandidateFailures  = 5
	domainHealthWorkerCount           = 6
	domainHealthDNSTimeout            = 5 * time.Second
	domainHealthTCPTimeout            = 4 * time.Second

	domainDNSStatusUnknown     = "unknown"
	domainDNSStatusOK          = "ok"
	domainDNSStatusNXDOMAIN    = "nxdomain"
	domainDNSStatusTimeout     = "timeout"
	domainDNSStatusServFail    = "servfail"
	domainDNSStatusNoAnswer    = "noanswer"
	domainDNSStatusError       = "error"
	domainDNSStatusStatic      = "static"
	domainDNSStatusRuntimeDown = "runtime_down"

	domainTransportStatusUnknown     = "unknown"
	domainTransportStatusOK          = "ok"
	domainTransportStatusTimeout     = "timeout"
	domainTransportStatusRefused     = "refused"
	domainTransportStatusUnreachable = "unreachable"
	domainTransportStatusError       = "error"
	domainTransportStatusStatic      = "static"
	domainTransportStatusRuntimeDown = "runtime_down"

	domainDecisionKeep            = "keep"
	domainDecisionReview          = "review"
	domainDecisionDeleteCandidate = "delete_candidate"
)

type DomainHealthRecord struct {
	Domain                          string   `json:"domain"`
	DirectDNSStatus                 string   `json:"directDnsStatus"`
	DirectTransportStatus           string   `json:"directTransportStatus"`
	VPNDNSStatus                    string   `json:"vpnDnsStatus"`
	VPNTransportStatus              string   `json:"vpnTransportStatus"`
	Decision                        string   `json:"decision"`
	ConsecutiveDirectDNSFailures    int      `json:"consecutiveDirectDnsFailures"`
	ConsecutiveDirectTransportFails int      `json:"consecutiveDirectTransportFailures"`
	ConsecutiveVPNDNSFailures       int      `json:"consecutiveVpnDnsFailures"`
	ConsecutiveVPNTransportFails    int      `json:"consecutiveVpnTransportFailures"`
	ChecksTotal                     int      `json:"checksTotal"`
	SuccessTotal                    int      `json:"successTotal"`
	LastCheckedAt                   string   `json:"lastCheckedAt"`
	LastDirectOKAt                  string   `json:"lastDirectOkAt"`
	LastVPNOKAt                     string   `json:"lastVpnOkAt"`
	DirectLastError                 string   `json:"directLastError"`
	VPNLastError                    string   `json:"vpnLastError"`
	LastDirectIPs                   []string `json:"lastDirectIps"`
	LastVPNIPs                      []string `json:"lastVpnIps"`
}

type DomainHealthResponse struct {
	Domains   []DomainHealthRecord `json:"domains"`
	Count     int                  `json:"count"`
	CheckedAt string               `json:"checkedAt,omitempty"`
}

type domainHealthStore struct {
	db          *sql.DB
	mu          sync.Mutex
	initialized bool
	initErr     error
}

type domainPathProbe struct {
	RuntimeRequired bool
	RuntimeReady    bool
	InterfaceName   string
	FWMark          string
}

type domainPathCheckResult struct {
	DNSStatus       string
	TransportStatus string
	IPs             []string
	LastError       string
}

type domainAssignment struct {
	ProviderID    string
	ProviderName  string
	ProviderType  string
	Location      string
	RuntimeReady  bool
	InterfaceName string
	FWMark        string
}

func newDomainHealthStore(db *sql.DB) *domainHealthStore {
	return &domainHealthStore{db: db}
}

func (s *Service) DomainHealth() (DomainHealthResponse, error) {
	if s.domainHealth == nil || s.domains == nil {
		return DomainHealthResponse{Domains: []DomainHealthRecord{}}, nil
	}

	entries, err := s.domains.List()
	if err != nil {
		return DomainHealthResponse{}, err
	}

	if err := s.domainHealth.PruneMissing(entries); err != nil {
		return DomainHealthResponse{}, err
	}

	existing, err := s.domainHealth.List(entries)
	if err != nil {
		return DomainHealthResponse{}, err
	}

	out := make([]DomainHealthRecord, 0, len(entries))
	for _, entry := range entries {
		record, ok := existing[entry]
		if !ok {
			record = defaultDomainHealthRecord(entry)
		}
		out = append(out, record)
	}

	return DomainHealthResponse{
		Domains: out,
		Count:   len(out),
	}, nil
}

func (s *Service) CheckDomainHealth(ctx context.Context, requested []string) (DomainHealthResponse, error) {
	if s.domainHealth == nil || s.domains == nil {
		return DomainHealthResponse{Domains: []DomainHealthRecord{}}, nil
	}

	targets, err := s.resolveRequestedDomainHealthTargets(requested)
	if err != nil {
		return DomainHealthResponse{}, err
	}
	if len(targets) == 0 {
		return DomainHealthResponse{Domains: []DomainHealthRecord{}}, nil
	}

	existing, err := s.domainHealth.List(targets)
	if err != nil {
		return DomainHealthResponse{}, err
	}

	assignments, err := s.resolveDomainHealthAssignments(targets)
	if err != nil {
		return DomainHealthResponse{}, err
	}

	records := s.runDomainHealthChecks(ctx, targets, existing, assignments)
	if err := s.domainHealth.Upsert(records); err != nil {
		return DomainHealthResponse{}, err
	}

	return DomainHealthResponse{
		Domains:   records,
		Count:     len(records),
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func (s *Service) SampleDomainHealth(ctx context.Context) error {
	if s.domainHealth == nil || s.domains == nil {
		return nil
	}

	entries, err := s.domains.List()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return nil
	}

	if err := s.domainHealth.PruneMissing(entries); err != nil {
		return err
	}

	_, err = s.CheckDomainHealth(ctx, entries)
	return err
}

func (s *Service) RunDomainHealthSampler(ctx context.Context) {
	if s.domainHealth == nil {
		return
	}

	enabled := false
	initialDeadline := time.Time{}

	for {
		interval := s.effectiveDomainHealthSampleInterval()
		if interval <= 0 {
			enabled = false
			initialDeadline = time.Time{}
			select {
			case <-ctx.Done():
				return
			case <-time.After(disabledSamplerPollInterval):
				continue
			}
		}

		if !enabled {
			enabled = true
			if s.effectiveDomainHealthInitialSampleEnabled() {
				initialDeadline = time.Now().Add(domainHealthInitialDelay)
			} else {
				initialDeadline = time.Time{}
			}
		}

		wait := interval
		fireInitial := false
		if !initialDeadline.IsZero() {
			untilInitial := time.Until(initialDeadline)
			if untilInitial <= 0 {
				wait = 0
				fireInitial = true
			} else if untilInitial < wait {
				wait = untilInitial
				fireInitial = true
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
			if fireInitial && !initialDeadline.IsZero() && !time.Now().Before(initialDeadline) {
				initialDeadline = time.Time{}
				if err := s.SampleDomainHealth(ctx); err != nil {
					log.Printf("domain health initial sample failed: %v", err)
				}
				continue
			}
			if err := s.SampleDomainHealth(ctx); err != nil {
				log.Printf("domain health sample failed: %v", err)
			}
		}
	}
}

func (s *Service) resolveRequestedDomainHealthTargets(requested []string) ([]string, error) {
	active, err := s.domains.List()
	if err != nil {
		return nil, err
	}
	if len(requested) == 0 {
		return active, nil
	}

	activeSet := make(map[string]struct{}, len(active))
	for _, domain := range active {
		activeSet[domain] = struct{}{}
	}

	normalized := domains.NormalizeEntries(requested)
	filtered := make([]string, 0, len(normalized))
	for _, domain := range normalized {
		if _, exists := activeSet[domain]; !exists {
			continue
		}
		filtered = append(filtered, domain)
	}
	return filtered, nil
}

func (s *Service) resolveDomainHealthAssignments(targets []string) (map[string]domainAssignment, error) {
	assignments := make(map[string]domainAssignment, len(targets))
	if s.state == nil {
		return assignments, nil
	}

	state, err := s.state.Load()
	if err != nil {
		return nil, err
	}

	providersByID := make(map[string]config.Provider, len(state.Providers))
	for _, provider := range state.Providers {
		providersByID[provider.ID] = provider
	}

	targetSet := make(map[string]struct{}, len(targets))
	for _, domain := range targets {
		targetSet[domain] = struct{}{}
	}

	openvpnByProvider, err := s.indexOpenVPNDomainHealthRuntime()
	if err != nil {
		return nil, err
	}

	subscriptionByLocation, err := s.indexSubscriptionDomainHealthRuntime()
	if err != nil {
		return nil, err
	}

	for _, rule := range state.Rules {
		if !rule.Enabled {
			continue
		}
		provider, ok := providersByID[rule.ProviderID]
		if !ok || !provider.Enabled {
			continue
		}

		assignment := domainAssignment{
			ProviderID:   provider.ID,
			ProviderName: provider.Name,
			ProviderType: string(provider.Type),
			Location:     firstNonEmpty(rule.SelectedLocation, provider.SelectedLocation),
		}

		switch provider.Type {
		case config.ProviderTypeOpenVPN:
			if snapshot, exists := openvpnByProvider[provider.ID]; exists {
				assignment.InterfaceName = strings.TrimSpace(snapshot.InterfaceName)
				assignment.FWMark = strings.TrimSpace(snapshot.FWMark)
				assignment.RuntimeReady = strings.EqualFold(strings.TrimSpace(snapshot.Status), "running") && assignment.InterfaceName != ""
			}
		case config.ProviderTypeSubscription:
			key := buildDomainHealthSubscriptionKey(provider.ID, assignment.Location)
			if snapshot, exists := subscriptionByLocation[key]; exists {
				assignment.InterfaceName = strings.TrimSpace(snapshot.InterfaceName)
				assignment.FWMark = strings.TrimSpace(snapshot.FWMark)
				assignment.RuntimeReady = strings.EqualFold(strings.TrimSpace(snapshot.Status), "running") && assignment.InterfaceName != ""
			}
		}

		for _, entry := range rule.Domains {
			normalized, _, err := domains.NormalizeEntry(entry)
			if err != nil {
				continue
			}
			if len(targetSet) > 0 {
				if _, exists := targetSet[normalized]; !exists {
					continue
				}
			}
			assignments[normalized] = assignment
		}
	}

	return assignments, nil
}

func (s *Service) indexOpenVPNDomainHealthRuntime() (map[string]openvpn.RuntimeSnapshot, error) {
	out := make(map[string]openvpn.RuntimeSnapshot)
	if s.openvpn == nil {
		return out, nil
	}

	snapshots, err := s.openvpn.Snapshots()
	if err != nil {
		return nil, err
	}
	for _, snapshot := range snapshots {
		out[strings.TrimSpace(snapshot.ProviderID)] = snapshot
	}
	return out, nil
}

func (s *Service) indexSubscriptionDomainHealthRuntime() (map[string]subscription.RuntimeSnapshot, error) {
	out := make(map[string]subscription.RuntimeSnapshot)
	if s.subscriptions == nil {
		return out, nil
	}

	snapshots, err := s.subscriptions.Snapshots()
	if err != nil {
		return nil, err
	}
	for _, snapshot := range snapshots {
		key := buildDomainHealthSubscriptionKey(snapshot.ProviderID, snapshot.Location)
		out[key] = snapshot
	}
	return out, nil
}

func buildDomainHealthSubscriptionKey(providerID string, location string) string {
	return strings.TrimSpace(providerID) + "\n" + strings.ToLower(strings.TrimSpace(location))
}

func (s *Service) runDomainHealthChecks(ctx context.Context, targets []string, existing map[string]DomainHealthRecord, assignments map[string]domainAssignment) []DomainHealthRecord {
	type outcome struct {
		domain string
		record DomainHealthRecord
	}

	jobs := make(chan string)
	results := make(chan outcome, len(targets))

	workerCount := domainHealthWorkerCount
	if len(targets) < workerCount {
		workerCount = len(targets)
	}
	if workerCount < 1 {
		workerCount = 1
	}

	var wg sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for domain := range jobs {
				record := existing[domain]
				if record.Domain == "" {
					record = defaultDomainHealthRecord(domain)
				}
				results <- outcome{
					domain: domain,
					record: s.evaluateDomainHealth(ctx, record, assignments[domain]),
				}
			}
		}()
	}

	for _, domain := range targets {
		jobs <- domain
	}
	close(jobs)

	go func() {
		wg.Wait()
		close(results)
	}()

	byDomain := make(map[string]DomainHealthRecord, len(targets))
	for result := range results {
		byDomain[result.domain] = result.record
	}

	ordered := make([]DomainHealthRecord, 0, len(targets))
	for _, domain := range targets {
		record, ok := byDomain[domain]
		if !ok {
			record = defaultDomainHealthRecord(domain)
		}
		ordered = append(ordered, record)
	}
	return ordered
}

func (s *Service) evaluateDomainHealth(ctx context.Context, record DomainHealthRecord, assignment domainAssignment) DomainHealthRecord {
	now := time.Now().UTC().Format(time.RFC3339)
	record.Domain = strings.TrimSpace(record.Domain)
	record.LastCheckedAt = now
	record.ChecksTotal++

	if domains.IsIPEntry(record.Domain) {
		record.DirectDNSStatus = domainDNSStatusStatic
		record.DirectTransportStatus = domainTransportStatusStatic
		record.VPNDNSStatus = domainDNSStatusStatic
		record.VPNTransportStatus = domainTransportStatusStatic
		record.Decision = domainDecisionKeep
		record.ConsecutiveDirectDNSFailures = 0
		record.ConsecutiveDirectTransportFails = 0
		record.ConsecutiveVPNDNSFailures = 0
		record.ConsecutiveVPNTransportFails = 0
		record.SuccessTotal++
		record.LastDirectOKAt = now
		record.LastVPNOKAt = now
		record.DirectLastError = ""
		record.VPNLastError = ""
		record.LastDirectIPs = []string{}
		record.LastVPNIPs = []string{}
		return record
	}

	direct := checkDomainPath(ctx, record.Domain, domainPathProbe{})
	vpn := checkDomainPath(ctx, record.Domain, domainPathProbe{
		RuntimeRequired: true,
		RuntimeReady:    assignment.RuntimeReady,
		InterfaceName:   assignment.InterfaceName,
		FWMark:          assignment.FWMark,
	})

	record.DirectDNSStatus = direct.DNSStatus
	record.DirectTransportStatus = direct.TransportStatus
	record.VPNDNSStatus = vpn.DNSStatus
	record.VPNTransportStatus = vpn.TransportStatus
	record.LastDirectIPs = append([]string(nil), direct.IPs...)
	record.LastVPNIPs = append([]string(nil), vpn.IPs...)
	record.DirectLastError = strings.TrimSpace(direct.LastError)
	record.VPNLastError = strings.TrimSpace(vpn.LastError)

	record.ConsecutiveDirectDNSFailures = nextDNSFailureStreak(record.ConsecutiveDirectDNSFailures, direct.DNSStatus)
	record.ConsecutiveVPNDNSFailures = nextDNSFailureStreak(record.ConsecutiveVPNDNSFailures, vpn.DNSStatus)
	record.ConsecutiveDirectTransportFails = nextTransportFailureStreak(record.ConsecutiveDirectTransportFails, direct.DNSStatus, direct.TransportStatus)
	record.ConsecutiveVPNTransportFails = nextTransportFailureStreak(record.ConsecutiveVPNTransportFails, vpn.DNSStatus, vpn.TransportStatus)

	if isDomainPathHealthy(direct.DNSStatus, direct.TransportStatus) {
		record.LastDirectOKAt = now
	}
	if isDomainPathHealthy(vpn.DNSStatus, vpn.TransportStatus) {
		record.LastVPNOKAt = now
	}
	if isDomainPathHealthy(direct.DNSStatus, direct.TransportStatus) || isDomainPathHealthy(vpn.DNSStatus, vpn.TransportStatus) {
		record.SuccessTotal++
	}

	record.Decision = recommendDomainDecision(record)
	return record
}

func nextDNSFailureStreak(current int, status string) int {
	if isDomainDNSSuccess(status) {
		return 0
	}
	return current + 1
}

func nextTransportFailureStreak(current int, dnsStatus string, transportStatus string) int {
	if !isDomainDNSSuccess(dnsStatus) {
		return 0
	}
	if isDomainTransportSuccess(transportStatus) {
		return 0
	}
	return current + 1
}

func checkDomainPath(ctx context.Context, domain string, probe domainPathProbe) domainPathCheckResult {
	if probe.RuntimeRequired && !probe.RuntimeReady {
		return domainPathCheckResult{
			DNSStatus:       domainDNSStatusRuntimeDown,
			TransportStatus: domainTransportStatusRuntimeDown,
			IPs:             []string{},
			LastError:       "assigned VPN runtime is not running",
		}
	}

	ips, dnsStatus, dnsErrText := resolveDomainIPs(ctx, domain, probe)
	if dnsStatus != domainDNSStatusOK {
		transportStatus := domainTransportStatusUnknown
		if dnsStatus == domainDNSStatusRuntimeDown {
			transportStatus = domainTransportStatusRuntimeDown
		}
		return domainPathCheckResult{
			DNSStatus:       dnsStatus,
			TransportStatus: transportStatus,
			IPs:             []string{},
			LastError:       dnsErrText,
		}
	}

	transportStatus, transportErrText := checkDomainTransport(ctx, ips, probe)
	return domainPathCheckResult{
		DNSStatus:       dnsStatus,
		TransportStatus: transportStatus,
		IPs:             ips,
		LastError:       strings.TrimSpace(transportErrText),
	}
}

func resolveDomainIPs(parent context.Context, domain string, probe domainPathProbe) ([]string, string, string) {
	ctx, cancel := context.WithTimeout(parent, domainHealthDNSTimeout)
	defer cancel()

	var (
		addrs []net.IP
		err   error
	)

	if probe.RuntimeRequired {
		dnsServer := resolveDomainHealthVPNDNSServer()
		resolver := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network string, _ string) (net.Conn, error) {
				dialer := newDomainPathDialer(domainHealthDNSTimeout, probe)
				if strings.HasPrefix(strings.ToLower(network), "tcp") {
					return dialer.DialContext(ctx, "tcp", dnsServer)
				}
				return dialer.DialContext(ctx, "udp", dnsServer)
			},
		}
		addrs, err = resolver.LookupIP(ctx, "ip4", domain)
	} else {
		addrs, err = net.DefaultResolver.LookupIP(ctx, "ip4", domain)
	}

	if ctx.Err() == context.DeadlineExceeded {
		return nil, domainDNSStatusTimeout, "dns lookup timeout"
	}
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) {
			switch {
			case dnsErr.IsNotFound:
				return nil, domainDNSStatusNXDOMAIN, strings.TrimSpace(dnsErr.Error())
			case dnsErr.IsTimeout:
				return nil, domainDNSStatusTimeout, strings.TrimSpace(dnsErr.Error())
			case strings.Contains(strings.ToLower(dnsErr.Err), "server misbehaving"):
				return nil, domainDNSStatusServFail, strings.TrimSpace(dnsErr.Error())
			}
		}
		return nil, domainDNSStatusError, strings.TrimSpace(err.Error())
	}

	ips := uniqueSortedIPv4Strings(addrs)
	if len(ips) == 0 {
		return nil, domainDNSStatusNoAnswer, "dns returned no IPv4 answers"
	}

	return ips, domainDNSStatusOK, ""
}

func checkDomainTransport(parent context.Context, ips []string, probe domainPathProbe) (string, string) {
	if len(ips) == 0 {
		return domainTransportStatusUnknown, ""
	}

	dialer := newDomainPathDialer(domainHealthTCPTimeout, probe)
	ports := []string{"443", "80"}

	lastStatus := domainTransportStatusUnknown
	lastMessage := ""
	for _, ip := range firstNStrings(ips, 3) {
		for _, port := range ports {
			ctx, cancel := context.WithTimeout(parent, domainHealthTCPTimeout)
			conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip, port))
			cancel()
			if err == nil {
				_ = conn.Close()
				return domainTransportStatusOK, ""
			}

			lastStatus = classifyTransportError(err)
			lastMessage = strings.TrimSpace(err.Error())
		}
	}

	return lastStatus, lastMessage
}

func newDomainPathDialer(timeout time.Duration, probe domainPathProbe) *net.Dialer {
	dialer := &net.Dialer{Timeout: timeout}
	if strings.TrimSpace(probe.InterfaceName) == "" && strings.TrimSpace(probe.FWMark) == "" {
		return dialer
	}

	dialer.Control = func(_ string, _ string, c syscall.RawConn) error {
		return applyDomainHealthSocketOptions(c, probe.InterfaceName, probe.FWMark)
	}
	return dialer
}

func uniqueSortedIPv4Strings(addrs []net.IP) []string {
	seen := make(map[string]struct{}, len(addrs))
	ips := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if addr == nil {
			continue
		}
		text := strings.TrimSpace(addr.String())
		if text == "" {
			continue
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		ips = append(ips, text)
	}

	sort.Strings(ips)
	return ips
}

func classifyTransportError(err error) string {
	if err == nil {
		return domainTransportStatusOK
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return domainTransportStatusTimeout
	}

	message := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(message, "refused"):
		return domainTransportStatusRefused
	case strings.Contains(message, "unreachable"), strings.Contains(message, "no route"), strings.Contains(message, "network is down"):
		return domainTransportStatusUnreachable
	default:
		return domainTransportStatusError
	}
}

func recommendDomainDecision(record DomainHealthRecord) string {
	if domains.IsIPEntry(record.Domain) {
		return domainDecisionKeep
	}

	directHealthy := isDomainPathHealthy(record.DirectDNSStatus, record.DirectTransportStatus)
	vpnHealthy := isDomainPathHealthy(record.VPNDNSStatus, record.VPNTransportStatus)

	switch {
	case vpnHealthy:
		return domainDecisionKeep
	case directHealthy:
		return domainDecisionReview
	case isCandidateDNSFailure(record.DirectDNSStatus) &&
		isCandidateDNSFailure(record.VPNDNSStatus) &&
		record.ConsecutiveDirectDNSFailures >= domainHealthDNSCandidateFailures &&
		record.ConsecutiveVPNDNSFailures >= domainHealthDNSCandidateFailures:
		return domainDecisionDeleteCandidate
	default:
		return domainDecisionReview
	}
}

func isDomainDNSSuccess(status string) bool {
	switch status {
	case domainDNSStatusOK, domainDNSStatusStatic:
		return true
	default:
		return false
	}
}

func isDomainTransportSuccess(status string) bool {
	switch status {
	case domainTransportStatusOK, domainTransportStatusUnknown, domainTransportStatusStatic:
		return true
	default:
		return false
	}
}

func isDomainPathHealthy(dnsStatus string, transportStatus string) bool {
	if dnsStatus == domainDNSStatusStatic {
		return true
	}
	if dnsStatus != domainDNSStatusOK {
		return false
	}
	return isDomainTransportSuccess(transportStatus)
}

func isCandidateDNSFailure(status string) bool {
	switch status {
	case domainDNSStatusNXDOMAIN, domainDNSStatusNoAnswer:
		return true
	default:
		return false
	}
}

func defaultDomainHealthRecord(domain string) DomainHealthRecord {
	record := DomainHealthRecord{
		Domain:                strings.TrimSpace(domain),
		DirectDNSStatus:       domainDNSStatusUnknown,
		DirectTransportStatus: domainTransportStatusUnknown,
		VPNDNSStatus:          domainDNSStatusUnknown,
		VPNTransportStatus:    domainTransportStatusUnknown,
		Decision:              domainDecisionReview,
		LastDirectIPs:         []string{},
		LastVPNIPs:            []string{},
	}
	if domains.IsIPEntry(record.Domain) {
		record.DirectDNSStatus = domainDNSStatusStatic
		record.DirectTransportStatus = domainTransportStatusStatic
		record.VPNDNSStatus = domainDNSStatusStatic
		record.VPNTransportStatus = domainTransportStatusStatic
		record.Decision = domainDecisionKeep
	}
	return record
}

func firstNStrings(values []string, limit int) []string {
	if limit <= 0 || len(values) <= limit {
		return values
	}
	return values[:limit]
}

func resolveDomainHealthSampleInterval() time.Duration {
	return resolveDomainHealthSampleIntervalWithFallback(defaultDomainHealthSampleInterval)
}

func resolveDomainHealthSampleIntervalWithFallback(fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv("VPN_MANAGER_DOMAIN_HEALTH_INTERVAL"))
	if raw == "" {
		return fallback
	}

	switch strings.ToLower(raw) {
	case "0", "off", "false", "disabled":
		return 0
	}

	parsed, err := time.ParseDuration(raw)
	if err == nil && parsed >= time.Hour {
		return parsed
	}

	if hours, err := strconv.Atoi(raw); err == nil && hours >= 1 {
		return time.Duration(hours) * time.Hour
	}

	return fallback
}

func domainHealthInitialSampleEnabled() bool {
	return resolveDomainHealthInitialSampleEnabledWithFallback(true)
}

func resolveDomainHealthInitialSampleEnabledWithFallback(fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("VPN_MANAGER_DOMAIN_HEALTH_INITIAL_SAMPLE")))
	switch raw {
	case "":
		return fallback
	case "1", "on", "true", "enabled":
		return true
	case "0", "off", "false", "disabled":
		return false
	default:
		return fallback
	}
}

func resolveDomainHealthVPNDNSServer() string {
	raw := strings.TrimSpace(os.Getenv("VPN_MANAGER_DOMAIN_HEALTH_VPN_DNS_SERVER"))
	if raw == "" {
		return "1.1.1.1:53"
	}
	if strings.Contains(raw, ":") {
		return raw
	}
	return net.JoinHostPort(raw, "53")
}

func (s *domainHealthStore) ensureReady() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.initialized {
		return s.initErr
	}
	s.initialized = true

	if s.db == nil {
		s.initErr = errors.New("domain health database is not configured")
		return s.initErr
	}

	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS domain_health (
			domain TEXT PRIMARY KEY,
			direct_dns_status TEXT NOT NULL DEFAULT 'unknown',
			direct_transport_status TEXT NOT NULL DEFAULT 'unknown',
			vpn_dns_status TEXT NOT NULL DEFAULT 'unknown',
			vpn_transport_status TEXT NOT NULL DEFAULT 'unknown',
			decision TEXT NOT NULL DEFAULT 'review',
			consecutive_direct_dns_failures INTEGER NOT NULL DEFAULT 0,
			consecutive_direct_transport_failures INTEGER NOT NULL DEFAULT 0,
			consecutive_vpn_dns_failures INTEGER NOT NULL DEFAULT 0,
			consecutive_vpn_transport_failures INTEGER NOT NULL DEFAULT 0,
			checks_total INTEGER NOT NULL DEFAULT 0,
			success_total INTEGER NOT NULL DEFAULT 0,
			last_checked_at TEXT NOT NULL DEFAULT '',
			last_direct_ok_at TEXT NOT NULL DEFAULT '',
			last_vpn_ok_at TEXT NOT NULL DEFAULT '',
			direct_last_error TEXT NOT NULL DEFAULT '',
			vpn_last_error TEXT NOT NULL DEFAULT '',
			last_direct_ips_json TEXT NOT NULL DEFAULT '[]',
			last_vpn_ips_json TEXT NOT NULL DEFAULT '[]'
		)`,
		`CREATE INDEX IF NOT EXISTS idx_domain_health_last_checked ON domain_health(last_checked_at)`,
		`CREATE INDEX IF NOT EXISTS idx_domain_health_decision ON domain_health(decision)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			s.initErr = err
			return err
		}
	}

	for _, column := range []struct {
		name       string
		definition string
	}{
		{name: "direct_dns_status", definition: "TEXT NOT NULL DEFAULT 'unknown'"},
		{name: "direct_transport_status", definition: "TEXT NOT NULL DEFAULT 'unknown'"},
		{name: "vpn_dns_status", definition: "TEXT NOT NULL DEFAULT 'unknown'"},
		{name: "vpn_transport_status", definition: "TEXT NOT NULL DEFAULT 'unknown'"},
		{name: "decision", definition: "TEXT NOT NULL DEFAULT 'review'"},
		{name: "consecutive_direct_dns_failures", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "consecutive_direct_transport_failures", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "consecutive_vpn_dns_failures", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "consecutive_vpn_transport_failures", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "checks_total", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "success_total", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "last_checked_at", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "last_direct_ok_at", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "last_vpn_ok_at", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "direct_last_error", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "vpn_last_error", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "last_direct_ips_json", definition: "TEXT NOT NULL DEFAULT '[]'"},
		{name: "last_vpn_ips_json", definition: "TEXT NOT NULL DEFAULT '[]'"},
	} {
		if err := s.ensureColumnUnlocked(column.name, column.definition); err != nil {
			s.initErr = err
			return err
		}
	}

	return nil
}

func (s *domainHealthStore) ensureColumnUnlocked(name string, definition string) error {
	rows, err := s.db.Query(`PRAGMA table_info(domain_health)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			columnName string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultVal, &pk); err != nil {
			return err
		}
		if strings.EqualFold(strings.TrimSpace(columnName), strings.TrimSpace(name)) {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	_, err = s.db.Exec(`ALTER TABLE domain_health ADD COLUMN ` + name + ` ` + definition)
	return err
}

func (s *domainHealthStore) List(domains []string) (map[string]DomainHealthRecord, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`
		SELECT
			domain,
			direct_dns_status,
			direct_transport_status,
			vpn_dns_status,
			vpn_transport_status,
			decision,
			consecutive_direct_dns_failures,
			consecutive_direct_transport_failures,
			consecutive_vpn_dns_failures,
			consecutive_vpn_transport_failures,
			checks_total,
			success_total,
			last_checked_at,
			last_direct_ok_at,
			last_vpn_ok_at,
			direct_last_error,
			vpn_last_error,
			last_direct_ips_json,
			last_vpn_ips_json
		FROM domain_health
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	filter := make(map[string]struct{}, len(domains))
	for _, domain := range domains {
		filter[domain] = struct{}{}
	}

	out := make(map[string]DomainHealthRecord, len(domains))
	for rows.Next() {
		record, err := scanDomainHealthRecord(rows)
		if err != nil {
			return nil, err
		}
		if len(filter) > 0 {
			if _, exists := filter[record.Domain]; !exists {
				continue
			}
		}
		out[record.Domain] = record
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return out, nil
}

func scanDomainHealthRecord(scanner interface{ Scan(dest ...any) error }) (DomainHealthRecord, error) {
	var (
		record           DomainHealthRecord
		lastDirectIPsRaw string
		lastVPNIPsRaw    string
	)

	if err := scanner.Scan(
		&record.Domain,
		&record.DirectDNSStatus,
		&record.DirectTransportStatus,
		&record.VPNDNSStatus,
		&record.VPNTransportStatus,
		&record.Decision,
		&record.ConsecutiveDirectDNSFailures,
		&record.ConsecutiveDirectTransportFails,
		&record.ConsecutiveVPNDNSFailures,
		&record.ConsecutiveVPNTransportFails,
		&record.ChecksTotal,
		&record.SuccessTotal,
		&record.LastCheckedAt,
		&record.LastDirectOKAt,
		&record.LastVPNOKAt,
		&record.DirectLastError,
		&record.VPNLastError,
		&lastDirectIPsRaw,
		&lastVPNIPsRaw,
	); err != nil {
		return DomainHealthRecord{}, err
	}

	if err := json.Unmarshal([]byte(strings.TrimSpace(lastDirectIPsRaw)), &record.LastDirectIPs); err != nil || record.LastDirectIPs == nil {
		record.LastDirectIPs = []string{}
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(lastVPNIPsRaw)), &record.LastVPNIPs); err != nil || record.LastVPNIPs == nil {
		record.LastVPNIPs = []string{}
	}

	return record, nil
}

func (s *domainHealthStore) Upsert(records []DomainHealthRecord) error {
	if err := s.ensureReady(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	for _, record := range records {
		lastDirectIPsJSON, err := json.Marshal(record.LastDirectIPs)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		lastVPNIPsJSON, err := json.Marshal(record.LastVPNIPs)
		if err != nil {
			_ = tx.Rollback()
			return err
		}

		if _, err := tx.Exec(`
			INSERT INTO domain_health (
				domain,
				direct_dns_status,
				direct_transport_status,
				vpn_dns_status,
				vpn_transport_status,
				decision,
				consecutive_direct_dns_failures,
				consecutive_direct_transport_failures,
				consecutive_vpn_dns_failures,
				consecutive_vpn_transport_failures,
				checks_total,
				success_total,
				last_checked_at,
				last_direct_ok_at,
				last_vpn_ok_at,
				direct_last_error,
				vpn_last_error,
				last_direct_ips_json,
				last_vpn_ips_json
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(domain) DO UPDATE SET
				direct_dns_status = excluded.direct_dns_status,
				direct_transport_status = excluded.direct_transport_status,
				vpn_dns_status = excluded.vpn_dns_status,
				vpn_transport_status = excluded.vpn_transport_status,
				decision = excluded.decision,
				consecutive_direct_dns_failures = excluded.consecutive_direct_dns_failures,
				consecutive_direct_transport_failures = excluded.consecutive_direct_transport_failures,
				consecutive_vpn_dns_failures = excluded.consecutive_vpn_dns_failures,
				consecutive_vpn_transport_failures = excluded.consecutive_vpn_transport_failures,
				checks_total = excluded.checks_total,
				success_total = excluded.success_total,
				last_checked_at = excluded.last_checked_at,
				last_direct_ok_at = excluded.last_direct_ok_at,
				last_vpn_ok_at = excluded.last_vpn_ok_at,
				direct_last_error = excluded.direct_last_error,
				vpn_last_error = excluded.vpn_last_error,
				last_direct_ips_json = excluded.last_direct_ips_json,
				last_vpn_ips_json = excluded.last_vpn_ips_json
		`, record.Domain, record.DirectDNSStatus, record.DirectTransportStatus, record.VPNDNSStatus, record.VPNTransportStatus, record.Decision, record.ConsecutiveDirectDNSFailures, record.ConsecutiveDirectTransportFails, record.ConsecutiveVPNDNSFailures, record.ConsecutiveVPNTransportFails, record.ChecksTotal, record.SuccessTotal, record.LastCheckedAt, record.LastDirectOKAt, record.LastVPNOKAt, record.DirectLastError, record.VPNLastError, string(lastDirectIPsJSON), string(lastVPNIPsJSON)); err != nil {
			_ = tx.Rollback()
			return err
		}
	}

	return tx.Commit()
}

func (s *domainHealthStore) PruneMissing(active []string) error {
	if err := s.ensureReady(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`SELECT domain FROM domain_health`)
	if err != nil {
		return err
	}
	defer rows.Close()

	activeSet := make(map[string]struct{}, len(active))
	for _, domain := range active {
		activeSet[domain] = struct{}{}
	}

	stale := make([]string, 0, 16)
	for rows.Next() {
		var domain string
		if err := rows.Scan(&domain); err != nil {
			return err
		}
		if _, exists := activeSet[domain]; exists {
			continue
		}
		stale = append(stale, domain)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, domain := range stale {
		if _, err := s.db.Exec(`DELETE FROM domain_health WHERE domain = ?`, domain); err != nil {
			return err
		}
	}

	return nil
}
