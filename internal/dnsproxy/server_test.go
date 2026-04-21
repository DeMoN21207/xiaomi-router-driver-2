package dnsproxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestEnabledFromEnv(t *testing.T) {
	t.Run("enabled by default", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DNS_PROXY", "")
		if !EnabledFromEnv() {
			t.Fatalf("expected dns proxy enabled by default")
		}
	})

	t.Run("explicit on enables proxy", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DNS_PROXY", "on")
		if !EnabledFromEnv() {
			t.Fatalf("expected dns proxy enabled")
		}
	})

	t.Run("invalid values keep proxy enabled", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DNS_PROXY", "maybe")
		if !EnabledFromEnv() {
			t.Fatalf("expected dns proxy enabled for invalid value")
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestResolveReturnsFastestSuccessfulUpstream(t *testing.T) {
	slowStarted := make(chan struct{})
	slowCanceled := make(chan struct{})
	var slowStartedOnce sync.Once
	var slowCanceledOnce sync.Once

	fastResponse := []byte{0xde, 0xad, 0xbe, 0xef}
	server := &Server{
		upstreams:  []string{"https://slow/dns-query", "https://fast/dns-query"},
		timeout:    500 * time.Millisecond,
		maxMessage: defaultMaxMessage,
		client: &http.Client{
			Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Host {
				case "slow":
					slowStartedOnce.Do(func() {
						close(slowStarted)
					})
					select {
					case <-request.Context().Done():
						slowCanceledOnce.Do(func() {
							close(slowCanceled)
						})
						return nil, request.Context().Err()
					case <-time.After(300 * time.Millisecond):
						return dnsHTTPResponse([]byte{0x01}), nil
					}
				case "fast":
					time.Sleep(20 * time.Millisecond)
					return dnsHTTPResponse(fastResponse), nil
				default:
					return nil, errors.New("unexpected upstream")
				}
			}),
		},
	}

	response, err := server.resolve(context.Background(), testDNSQuery())
	if err != nil {
		t.Fatalf("resolve() error = %v", err)
	}
	if !bytes.Equal(response, fastResponse) {
		t.Fatalf("resolve() response = %x, want %x", response, fastResponse)
	}

	select {
	case <-slowStarted:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("slow upstream did not start")
	}

	select {
	case <-slowCanceled:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("slow upstream was not canceled after fast success")
	}
}

func TestResolveUsesSingleTimeoutBudget(t *testing.T) {
	server := &Server{
		upstreams:  []string{"https://slow-a/dns-query", "https://slow-b/dns-query"},
		timeout:    30 * time.Millisecond,
		maxMessage: defaultMaxMessage,
		client: &http.Client{
			Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				<-request.Context().Done()
				return nil, request.Context().Err()
			}),
		},
	}

	startedAt := time.Now()
	_, err := server.resolve(context.Background(), testDNSQuery())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("resolve() error = %v, want context deadline exceeded", err)
	}

	if elapsed := time.Since(startedAt); elapsed > 55*time.Millisecond {
		t.Fatalf("resolve() elapsed = %v, want <= 55ms", elapsed)
	}
}

func TestHandleTCPConnRefreshesDeadlinesBetweenQueries(t *testing.T) {
	query := testDNSQuery()
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()

	server := &Server{
		upstreams:  []string{"https://echo/dns-query"},
		timeout:    60 * time.Millisecond,
		maxMessage: defaultMaxMessage,
		client: &http.Client{
			Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					return nil, err
				}
				time.Sleep(40 * time.Millisecond)
				return dnsHTTPResponse(body), nil
			}),
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		server.handleTCPConn(ctx, serverConn)
	}()

	if err := clientConn.SetDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatalf("SetDeadline() error = %v", err)
	}
	if err := writeTCPDNSMessage(clientConn, query); err != nil {
		t.Fatalf("writeTCPDNSMessage(first) error = %v", err)
	}

	firstResponse, err := readTCPDNSMessage(clientConn)
	if err != nil {
		t.Fatalf("readTCPDNSMessage(first) error = %v", err)
	}
	if !bytes.Equal(firstResponse, query) {
		t.Fatalf("first response = %x, want %x", firstResponse, query)
	}

	time.Sleep(30 * time.Millisecond)

	if err := clientConn.SetDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatalf("SetDeadline() second error = %v", err)
	}
	if err := writeTCPDNSMessage(clientConn, query); err != nil {
		t.Fatalf("writeTCPDNSMessage(second) error = %v", err)
	}

	secondResponse, err := readTCPDNSMessage(clientConn)
	if err != nil {
		t.Fatalf("readTCPDNSMessage(second) error = %v", err)
	}
	if !bytes.Equal(secondResponse, query) {
		t.Fatalf("second response = %x, want %x", secondResponse, query)
	}

	cancel()
	_ = clientConn.Close()

	select {
	case <-done:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("handleTCPConn() did not exit after client close")
	}
}

func dnsHTTPResponse(body []byte) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func testDNSQuery() []byte {
	return []byte{
		0x12, 0x34, 0x01, 0x00,
		0x00, 0x01, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
		0x07, 'e', 'x', 'a', 'm', 'p', 'l', 'e',
		0x03, 'c', 'o', 'm',
		0x00,
		0x00, 0x01,
		0x00, 0x01,
	}
}

func writeTCPDNSMessage(conn net.Conn, message []byte) error {
	var lengthHeader [2]byte
	binary.BigEndian.PutUint16(lengthHeader[:], uint16(len(message)))
	if _, err := conn.Write(lengthHeader[:]); err != nil {
		return err
	}
	_, err := conn.Write(message)
	return err
}

func readTCPDNSMessage(conn net.Conn) ([]byte, error) {
	var lengthHeader [2]byte
	if _, err := io.ReadFull(conn, lengthHeader[:]); err != nil {
		return nil, err
	}

	message := make([]byte, int(binary.BigEndian.Uint16(lengthHeader[:])))
	if _, err := io.ReadFull(conn, message); err != nil {
		return nil, err
	}

	return message, nil
}
