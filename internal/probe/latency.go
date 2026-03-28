package probe

import (
	"context"
	"net"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	latencyConcurrency = 8
	latencyTimeout     = 3 * time.Second
)

type latencyResult struct {
	latencyMs int64
	err       string
}

// MeasureLatencies resolves network latency for the provided locations by
// calling the system ping binary once per unique host.
func MeasureLatencies(ctx context.Context, locations []Location) []Location {
	if len(locations) == 0 {
		return []Location{}
	}

	hosts := make([]string, 0, len(locations))
	seenHosts := make(map[string]struct{}, len(locations))
	for _, location := range locations {
		host := extractHost(location.Address)
		if host == "" {
			continue
		}
		if _, exists := seenHosts[host]; exists {
			continue
		}
		seenHosts[host] = struct{}{}
		hosts = append(hosts, host)
	}

	results := make(map[string]latencyResult, len(hosts))
	if len(hosts) > 0 {
		pingBinary, err := exec.LookPath("ping")
		if err != nil {
			for _, host := range hosts {
				results[host] = latencyResult{err: "ping binary not found"}
			}
		} else {
			workerCount := latencyConcurrency
			if len(hosts) < workerCount {
				workerCount = len(hosts)
			}

			var mu sync.Mutex
			hostCh := make(chan string)
			wg := sync.WaitGroup{}
			for worker := 0; worker < workerCount; worker++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for host := range hostCh {
						latency, pingErr := pingHost(ctx, pingBinary, host)
						mu.Lock()
						results[host] = latencyResult{latencyMs: latency, err: pingErr}
						mu.Unlock()
					}
				}()
			}

			for _, host := range hosts {
				hostCh <- host
			}
			close(hostCh)
			wg.Wait()
		}
	}

	out := make([]Location, len(locations))
	for index, location := range locations {
		location.LatencyMs = 0
		location.LatencyError = ""

		host := extractHost(location.Address)
		switch {
		case host == "":
			location.LatencyError = "host is empty"
		default:
			result := results[host]
			location.LatencyMs = result.latencyMs
			location.LatencyError = result.err
		}

		out[index] = location
	}

	return out
}

func pingHost(parent context.Context, pingBinary string, host string) (int64, string) {
	ctx, cancel := context.WithTimeout(parent, latencyTimeout)
	defer cancel()

	args := []string{"-c", "1", "-W", "2", host}
	if runtime.GOOS == "windows" {
		args = []string{"-n", "1", "-w", "1500", host}
	}

	cmd := exec.CommandContext(ctx, pingBinary, args...)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return 0, "timeout"
	}
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return 0, message
	}

	latency := parsePingLatency(string(output))
	if latency <= 0 {
		return 0, "latency unavailable"
	}

	return latency, ""
}

func parsePingLatency(output string) int64 {
	candidates := []string{"time=", "time<"}
	for _, candidate := range candidates {
		index := strings.Index(output, candidate)
		if index < 0 {
			continue
		}

		rest := output[index+len(candidate):]
		end := strings.IndexAny(rest, " \n\r\tm")
		if end < 0 {
			end = len(rest)
		}

		value := strings.Trim(rest[:end], "<>=")
		if value == "" {
			continue
		}

		parsed, err := strconv.ParseFloat(strings.ReplaceAll(value, ",", "."), 64)
		if err != nil {
			continue
		}
		if parsed < 1 {
			return 1
		}
		return int64(parsed)
	}

	patterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)([0-9]+(?:[.,][0-9]+)?)\D+TTL=`),
		regexp.MustCompile(`(?i)Average\s*=\s*([0-9]+(?:[.,][0-9]+)?)`),
	}
	for _, pattern := range patterns {
		matches := pattern.FindStringSubmatch(output)
		if len(matches) < 2 {
			continue
		}

		parsed, err := strconv.ParseFloat(strings.ReplaceAll(matches[1], ",", "."), 64)
		if err != nil {
			continue
		}
		return int64(parsed)
	}

	return 0
}

func extractHost(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}

	if host, _, err := net.SplitHostPort(address); err == nil {
		return strings.Trim(host, "[]")
	}

	if strings.HasPrefix(address, "[") {
		if end := strings.Index(address, "]"); end > 1 {
			return address[1:end]
		}
	}

	if strings.Count(address, ":") == 1 {
		parts := strings.SplitN(address, ":", 2)
		if strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" {
			return strings.TrimSpace(parts[0])
		}
	}

	return address
}
