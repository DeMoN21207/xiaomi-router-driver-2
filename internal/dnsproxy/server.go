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
	defaultAddr       = "127.0.0.1:15353"
	defaultTimeout    = 8 * time.Second
	defaultMaxMessage = 65535
)

var defaultUpstreams = []string{
	"https://cloudflare-dns.com/dns-query",
	"https://dns.google/dns-query",
}

type Config struct {
	Addr       string
	Upstreams  []string
	Timeout    time.Duration
	MaxMessage int
}

type Server struct {
	addr       string
	upstreams  []string
	timeout    time.Duration
	maxMessage int
	client     *http.Client
	udpConn    net.PacketConn
	tcpLn      net.Listener
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

func EnabledFromEnv() bool {
	value := strings.TrimSpace(os.Getenv("VPN_MANAGER_DNS_PROXY"))
	if value == "" {
		return true
	}

	switch strings.ToLower(value) {
	case "0", "false", "no", "off", "disabled":
		return false
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

	return Config{
		Addr:       addr,
		Upstreams:  upstreams,
		Timeout:    timeout,
		MaxMessage: defaultMaxMessage,
	}
}

func New(config Config) (*Server, error) {
	config = normalizeConfig(config)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment

	return &Server{
		addr:       config.Addr,
		upstreams:  config.Upstreams,
		timeout:    config.Timeout,
		maxMessage: config.MaxMessage,
		client: &http.Client{
			Timeout:   config.Timeout,
			Transport: transport,
		},
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
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			response := s.resolveOrServFail(ctx, query)
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
	_ = conn.SetDeadline(time.Now().Add(s.timeout))

	for {
		var lengthHeader [2]byte
		if _, err := io.ReadFull(conn, lengthHeader[:]); err != nil {
			return
		}

		length := int(binary.BigEndian.Uint16(lengthHeader[:]))
		if length == 0 || length > s.maxMessage {
			return
		}

		query := make([]byte, length)
		if _, err := io.ReadFull(conn, query); err != nil {
			return
		}

		response := s.resolveOrServFail(ctx, query)
		if len(response) == 0 || len(response) > 65535 {
			return
		}

		binary.BigEndian.PutUint16(lengthHeader[:], uint16(len(response)))
		if _, err := conn.Write(lengthHeader[:]); err != nil {
			return
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

func (s *Server) resolve(ctx context.Context, query []byte) ([]byte, error) {
	var lastErr error
	for _, upstream := range s.upstreams {
		response, err := s.resolveOnce(ctx, upstream, query)
		if err == nil {
			return response, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no DNS-over-HTTPS upstreams configured")
	}
	return nil, lastErr
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

	return config
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
