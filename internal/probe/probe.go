package probe

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"xiomi-router-driver/internal/subscription"
)

// Location represents a single discovered endpoint from a provider source.
type Location struct {
	Name         string `json:"name"`
	Address      string `json:"address,omitempty"`
	Type         string `json:"type,omitempty"`
	LatencyMs    int64  `json:"latencyMs,omitempty"`
	LatencyError string `json:"latencyError,omitempty"`
}

// Result holds the probe outcome.
type Result struct {
	Locations []Location `json:"locations"`
	RawCount  int        `json:"rawCount"`
	Error     string     `json:"error,omitempty"`
}

// ProbeSource inspects a provider source and returns discovered locations.
// providerType is "openvpn" or "subscription".
// baseDir is used to resolve relative provider-local files like uploaded .ovpn profiles.
func ProbeSource(providerType, source, baseDir string) Result {
	source = strings.TrimSpace(source)
	if source == "" {
		return Result{Error: "source is required"}
	}

	switch providerType {
	case "openvpn":
		return probeOpenVPN(source, baseDir)
	case "subscription":
		return probeSubscription(source)
	default:
		return Result{Error: fmt.Sprintf("unsupported provider type: %s", providerType)}
	}
}

func probeOpenVPN(source, baseDir string) Result {
	path := source
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}

	file, err := os.Open(path)
	if err != nil {
		return Result{Error: fmt.Sprintf("open profile: %v", err)}
	}
	defer file.Close()

	remotes := make([]Location, 0, 4)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "remote ") {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		address := parts[1]
		if len(parts) >= 3 && strings.TrimSpace(parts[2]) != "" {
			address += ":" + parts[2]
		}
		remotes = append(remotes, Location{
			Name:    parts[1],
			Address: address,
			Type:    "openvpn",
		})
	}

	if len(remotes) == 0 {
		return Result{
			Locations: []Location{},
			RawCount:  0,
			Error:     "no remote directives found in .ovpn profile",
		}
	}

	return Result{Locations: remotes, RawCount: len(remotes)}
}

func probeSubscription(source string) Result {
	entries, err := subscription.FetchEntries(source)
	if err != nil {
		return Result{Error: err.Error()}
	}

	locations := make([]Location, 0, len(entries))
	for _, entry := range entries {
		locations = append(locations, Location{
			Name:    entry.Name,
			Address: entry.Address,
			Type:    entry.Type,
		})
	}

	return Result{
		Locations: locations,
		RawCount:  len(entries),
	}
}
