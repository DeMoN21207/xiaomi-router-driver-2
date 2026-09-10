package dnsproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultAddr                = "127.0.0.1:15353"
	defaultTimeout             = 8 * time.Second
	defaultMaxMessage          = 65535
	defaultMaxIdleConnsPerHost = 8
	defaultMaxConcurrent       = 64
	dohFailureThreshold        = 3
	dohCircuitCooldown         = 30 * time.Second
)

var defaultUpstreams = []string{
	"https://cloudflare-dns.com/dns-query",
	"https://dns.google/dns-query",
}

var defaultFallbacks = []string{"1.1.1.1:53", "8.8.8.8:53"}

type Config struct {
	Addr          string
	Upstreams     []string
	Timeout       time.Duration
	MaxMessage    int
	MaxConcurrent int
	Fallbacks     []string
}

type Server struct {
	addr              string
	upstreams         []string
	timeout           time.Duration
	maxMessage        int
	client            *http.Client
	fallbacks         []string
	querySlots        chan struct{}
	slotOnce          sync.Once
	plainResolve      func(context.Context, string, []byte) ([]byte, error)
	dohMu             sync.Mutex
	dohFailures       int
	dohOpenUntil      time.Time
	fallbackSuccesses uint64
	lastFallbackAt    time.Time
	lastError         string
	udpConn           net.PacketConn
	tcpLn             net.Listener
	cancel            context.CancelFunc
	wg                sync.WaitGroup
}

type resolveResult struct {
	response []byte
	err      error
}

type Health struct {
	State             string   `json:"state"`
	Failures          int      `json:"failures"`
	OpenUntil         string   `json:"openUntil,omitempty"`
	InFlight          int      `json:"inFlight"`
	MaxConcurrent     int      `json:"maxConcurrent"`
	Fallbacks         []string `json:"fallbacks"`
	FallbackSuccesses uint64   `json:"fallbackSuccesses"`
	LastFallbackAt    string   `json:"lastFallbackAt,omitempty"`
	LastError         string   `json:"lastError,omitempty"`
}

func EnabledFromEnv() bool {
	value := strings.TrimSpace(os.Getenv("VPN_MANAGER_DNS_PROXY"))
	if value == "" {
		return true
	}

	switch strings.ToLower(value) {
	case "0", "false", "no", "off", "disabled":
		return false
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return true
	}
}

func ConfigFromEnv() Config {
	addr := strings.TrimSpace(os.Getenv("VPN_MANAGER_DNS_PROXY_ADDR"))
	if addr == "" {
		addr = defaultAddr
	}

	upstreams := splitList(os.Getenv("VPN_MANAGER_DNS_PROXY_UPSTREAMS"))
	if len(upstreams) == 0 {
		upstream := strings.TrimSpace(os.Getenv("VPN_MANAGER_DNS_PROXY_UPSTREAM"))
		if upstream != "" {
			upstreams = []string{upstream}
		} else {
			upstreams = append([]string(nil), defaultUpstreams...)
		}
	}

	timeout := defaultTimeout
	if value := strings.TrimSpace(os.Getenv("VPN_MANAGER_DNS_PROXY_TIMEOUT")); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
			timeout = time.Duration(seconds) * time.Second
		}
	}
	fallbacks := splitList(os.Getenv("VPN_MANAGER_DNS_PROXY_FALLBACKS"))
	if len(fallbacks) == 0 {
		fallbacks = append([]string(nil), defaultFallbacks...)
	}

	return Config{
		Addr:          addr,
		Upstreams:     upstreams,
		Timeout:       timeout,
		MaxMessage:    defaultMaxMessage,
		MaxConcurrent: resolvePositiveIntEnv("VPN_MANAGER_DNS_PROXY_MAX_CONCURRENT", defaultMaxConcurrent),
		Fallbacks:     fallbacks,
	}
}

func New(config Config) (*Server, error) {
	config = normalizeConfig(config)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	transport.MaxIdleConnsPerHost = max(defaultMaxIdleConnsPerHost, len(config.Upstreams))

	return &Server{
		addr:       config.Addr,
		upstreams:  config.Upstreams,
		timeout:    config.Timeout,
		maxMessage: config.MaxMessage,
		client: &http.Client{
			Timeout:   config.Timeout,
			Transport: transport,
		},
		fallbacks:    append([]string(nil), config.Fallbacks...),
		querySlots:   make(chan struct{}, config.MaxConcurrent),
		plainResolve: resolvePlainDNS,
	}, nil
}

func Start(ctx context.Context, config Config) (*Server, error) {
	server, err := New(config)
	if err != nil {
		return nil, err
	}
	if err := server.Start(ctx); err != nil {
		return nil, err
	}
	return server, nil
}

func (s *Server) Start(ctx context.Context) error {
	if s == nil {
		return errors.New("dns proxy server is nil")
	}

	udpConn, err := net.ListenPacket("udp", s.addr)
	if err != nil {
		return fmt.Errorf("listen udp %s: %w", s.addr, err)
	}

	tcpLn, err := net.Listen("tcp", s.addr)
	if err != nil {
		_ = udpConn.Close()
		return fmt.Errorf("listen tcp %s: %w", s.addr, err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	s.udpConn = udpConn
	s.tcpLn = tcpLn
	s.cancel = cancel

	s.wg.Add(2)
	go s.serveUDP(runCtx)
	go s.serveTCP(runCtx)

	go func() {
		<-runCtx.Done()
		_ = udpConn.Close()
		_ = tcpLn.Close()
	}()

	return nil
}

func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.udpConn != nil {
		_ = s.udpConn.Close()
	}
	if s.tcpLn != nil {
		_ = s.tcpLn.Close()
	}
	s.wg.Wait()
	return nil
}

func (s *Server) DnsmasqServer() string {
	host, port, err := net.SplitHostPort(s.addr)
	if err != nil {
		return ""
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return host + "#" + port
}

func (s *Server) Health() Health {
	if s == nil {
		return Health{State: "disabled"}
	}
	s.ensureQuerySlots()
	s.dohMu.Lock()
	defer s.dohMu.Unlock()
	state := "healthy"
	if !s.dohOpenUntil.IsZero() && time.Now().Before(s.dohOpenUntil) {
		state = "fallback"
	} else if s.dohFailures > 0 {
		state = "degraded"
	}
	health := Health{
		State: state, Failures: s.dohFailures, InFlight: len(s.querySlots), MaxConcurrent: cap(s.querySlots),
		Fallbacks: append([]string(nil), s.fallbacks...), FallbackSuccesses: s.fallbackSuccesses, LastError: s.lastError,
	}
	if !s.dohOpenUntil.IsZero() {
		health.OpenUntil = s.dohOpenUntil.UTC().Format(time.RFC3339)
	}
	if !s.lastFallbackAt.IsZero() {
		health.LastFallbackAt = s.lastFallbackAt.UTC().Format(time.RFC3339)
	}
	return health
}

func (s *Server) serveUDP(ctx context.Context) {
	defer s.wg.Done()

	buffer := make([]byte, s.maxMessage)
	for {
		n, remoteAddr, err := s.udpConn.ReadFrom(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("dns proxy udp read: %v", err)
			continue
		}

		query := append([]byte(nil), buffer[:n]...)
		if !s.tryAcquireQuerySlot() {
			if response := servFailResponse(query); len(response) > 0 {
				_, _ = s.udpConn.WriteTo(response, remoteAddr)
			}
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.releaseQuerySlot()
			response := s.resolveOrServFailWithinSlot(ctx, query)
			if len(response) == 0 {
				return
			}
			if _, err := s.udpConn.WriteTo(response, remoteAddr); err != nil && ctx.Err() == nil {
				log.Printf("dns proxy udp write: %v", err)
			}
		}()
	}
}

func (s *Server) serveTCP(ctx context.Context) {
	defer s.wg.Done()

	for {
		conn, err := s.tcpLn.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("dns proxy tcp accept: %v", err)
			continue
		}

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleTCPConn(ctx, conn)
		}()
	}
}

func (s *Server) handleTCPConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()

	for {
		var lengthHeader [2]byte
		if s.timeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(s.timeout))
		}
		if _, err := io.ReadFull(conn, lengthHeader[:]); err != nil {
			return
		}

		length := int(binary.BigEndian.Uint16(lengthHeader[:]))
		if length == 0 || length > s.maxMessage {
			return
		}

		query := make([]byte, length)
		if s.timeout > 0 {
			_ = conn.SetReadDeadline(time.Now().Add(s.timeout))
		}
		if _, err := io.ReadFull(conn, query); err != nil {
			return
		}

		response := s.resolveOrServFail(ctx, query)
		if len(response) == 0 || len(response) > 65535 {
			return
		}

		binary.BigEndian.PutUint16(lengthHeader[:], uint16(len(response)))
		if s.timeout > 0 {
			_ = conn.SetWriteDeadline(time.Now().Add(s.timeout))
		}
		if _, err := conn.Write(lengthHeader[:]); err != nil {
			return
		}
		if s.timeout > 0 {
			_ = conn.SetWriteDeadline(time.Now().Add(s.timeout))
		}
		if _, err := conn.Write(response); err != nil {
			return
		}
	}
}

func (s *Server) resolveOrServFail(ctx context.Context, query []byte) []byte {
	response, err := s.resolve(ctx, query)
	if err == nil {
		return response
	}
	if ctx.Err() == nil {
		log.Printf("dns proxy doh query failed: %v", err)
	}
	return servFailResponse(query)
}

func (s *Server) resolveOrServFailWithinSlot(ctx context.Context, query []byte) []byte {
	response, err := s.resolveWithinSlot(ctx, query)
	if err == nil {
		return response
	}
	if ctx.Err() == nil {
		log.Printf("dns proxy query failed: %v", err)
	}
	return servFailResponse(query)
}

func (s *Server) resolve(ctx context.Context, query []byte) ([]byte, error) {
	if err := s.acquireQuerySlot(ctx); err != nil {
		return nil, err
	}
	defer s.releaseQuerySlot()
	return s.resolveWithinSlot(ctx, query)
}

func (s *Server) resolveWithinSlot(ctx context.Context, query []byte) ([]byte, error) {
	if s.dohCircuitOpen(time.Now()) {
		return s.resolveFallback(ctx, query, errors.New("DNS-over-HTTPS circuit is open"))
	}
	if len(s.upstreams) == 0 {
		return s.resolveFallback(ctx, query, errors.New("no DNS-over-HTTPS upstreams configured"))
	}

	queryCtx, cancel := s.withQueryTimeout(ctx)
	defer cancel()

	raceCtx, raceCancel := context.WithCancel(queryCtx)
	defer raceCancel()

	results := make(chan resolveResult, len(s.upstreams))
	for _, upstream := range s.upstreams {
		upstream := upstream
		go func() {
			response, err := s.resolveOnce(raceCtx, upstream, query)
			results <- resolveResult{response: response, err: err}
		}()
	}

	errs := make([]error, 0, len(s.upstreams)+1)
	for range s.upstreams {
		select {
		case <-queryCtx.Done():
			errs = append(errs, queryCtx.Err())
			s.recordDoHFailure(time.Now())
			return nil, errors.Join(errs...)
		case result := <-results:
			if result.err == nil {
				s.recordDoHSuccess()
				raceCancel()
				return result.response, nil
			}
			errs = append(errs, result.err)
		}
	}

	s.recordDoHFailure(time.Now())
	return s.resolveFallback(queryCtx, query, errors.Join(errs...))
}

func (s *Server) dohCircuitOpen(now time.Time) bool {
	s.dohMu.Lock()
	defer s.dohMu.Unlock()
	if s.dohOpenUntil.IsZero() {
		return false
	}
	if now.Before(s.dohOpenUntil) {
		return true
	}
	s.dohOpenUntil = time.Time{}
	s.dohFailures = 0
	return false
}

func (s *Server) recordDoHSuccess() {
	s.dohMu.Lock()
	s.dohFailures = 0
	s.dohOpenUntil = time.Time{}
	s.lastError = ""
	s.dohMu.Unlock()
}

func (s *Server) recordDoHFailure(now time.Time) {
	s.dohMu.Lock()
	defer s.dohMu.Unlock()
	s.dohFailures++
	s.lastError = "all DNS-over-HTTPS upstreams failed"
	if s.dohFailures >= dohFailureThreshold {
		s.dohOpenUntil = now.Add(dohCircuitCooldown)
	}
}

func (s *Server) resolveFallback(ctx context.Context, query []byte, dohErr error) ([]byte, error) {
	resolver := s.plainResolve
	if resolver == nil {
		resolver = resolvePlainDNS
	}
	errs := []error{dohErr}
	for _, fallback := range s.fallbacks {
		response, err := resolver(ctx, fallback, query)
		if err == nil {
			s.dohMu.Lock()
			s.fallbackSuccesses++
			s.lastFallbackAt = time.Now()
			s.dohMu.Unlock()
			return response, nil
		}
		errs = append(errs, fmt.Errorf("fallback %s: %w", fallback, err))
	}
	return nil, errors.Join(errs...)
}

func (s *Server) acquireQuerySlot(ctx context.Context) error {
	s.ensureQuerySlots()
	select {
	case s.querySlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) tryAcquireQuerySlot() bool {
	s.ensureQuerySlots()
	select {
	case s.querySlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Server) releaseQuerySlot() {
	<-s.querySlots
}

func (s *Server) ensureQuerySlots() {
	s.slotOnce.Do(func() {
		if s.querySlots == nil {
			s.querySlots = make(chan struct{}, defaultMaxConcurrent)
		}
	})
}

func resolvePlainDNS(ctx context.Context, server string, query []byte) ([]byte, error) {
	if _, _, err := net.SplitHostPort(server); err != nil {
		server = net.JoinHostPort(server, "53")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "udp", server)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(defaultTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	if _, err := conn.Write(query); err != nil {
		return nil, err
	}
	response := make([]byte, defaultMaxMessage)
	n, err := conn.Read(response)
	if err != nil {
		return nil, err
	}
	if n < 12 || len(query) < 2 || response[0] != query[0] || response[1] != query[1] {
		return nil, errors.New("invalid plain DNS response")
	}
	return append([]byte(nil), response[:n]...), nil
}

func (s *Server) resolveOnce(ctx context.Context, upstream string, query []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, upstream, bytes.NewReader(query))
	if err != nil {
		return nil, err
	}
	request.Header.Set("accept", "application/dns-message")
	request.Header.Set("content-type", "application/dns-message")

	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, int64(s.maxMessage)+1))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("%s returned HTTP %d", upstream, response.StatusCode)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("%s returned an empty DNS response", upstream)
	}
	if len(body) > s.maxMessage {
		return nil, fmt.Errorf("%s returned an oversized DNS response", upstream)
	}

	return body, nil
}

func servFailResponse(query []byte) []byte {
	if len(query) < 12 {
		return nil
	}

	end := questionEnd(query)
	if end < 12 {
		end = 12
	}

	response := make([]byte, end)
	copy(response, query[:end])
	response[2] = 0x80 | (query[2] & 0x01)
	response[3] = 0x80 | 0x02
	response[6], response[7] = 0, 0
	response[8], response[9] = 0, 0
	response[10], response[11] = 0, 0
	return response
}

func questionEnd(message []byte) int {
	if len(message) < 12 {
		return -1
	}

	offset := 12
	for offset < len(message) {
		labelLength := int(message[offset])
		offset++
		if labelLength == 0 {
			break
		}
		if labelLength&0xc0 != 0 {
			return -1
		}
		offset += labelLength
		if offset > len(message) {
			return -1
		}
	}

	if offset+4 > len(message) {
		return -1
	}
	return offset + 4
}

func normalizeConfig(config Config) Config {
	config.Addr = strings.TrimSpace(config.Addr)
	if config.Addr == "" {
		config.Addr = defaultAddr
	}

	config.Upstreams = normalizeUpstreams(config.Upstreams)
	if len(config.Upstreams) == 0 {
		config.Upstreams = append([]string(nil), defaultUpstreams...)
	}

	if config.Timeout <= 0 {
		config.Timeout = defaultTimeout
	}
	if config.MaxMessage <= 0 || config.MaxMessage > defaultMaxMessage {
		config.MaxMessage = defaultMaxMessage
	}
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = defaultMaxConcurrent
	}
	if config.MaxConcurrent > 1024 {
		config.MaxConcurrent = 1024
	}
	config.Fallbacks = normalizeFallbacks(config.Fallbacks)
	if len(config.Fallbacks) == 0 {
		config.Fallbacks = append([]string(nil), defaultFallbacks...)
	}

	return config
}

func normalizeFallbacks(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(value); err != nil {
			value = net.JoinHostPort(strings.Trim(value, "[]"), "53")
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func resolvePositiveIntEnv(name string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func normalizeUpstreams(upstreams []string) []string {
	out := make([]string, 0, len(upstreams))
	for _, upstream := range upstreams {
		upstream = strings.TrimSpace(upstream)
		if upstream == "" {
			continue
		}
		out = append(out, upstream)
	}
	return out
}

func splitList(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	return normalizeUpstreams(parts)
}

func (s *Server) withQueryTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if s.timeout <= 0 {
		return ctx, func() {}
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= s.timeout {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, s.timeout)
}
