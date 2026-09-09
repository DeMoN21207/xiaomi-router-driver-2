package routing

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// CaptureIPSet prepares a private restore file before the old route is removed.
// It keeps remaining TTLs so a switch does not prolong stale DNS mappings.
func CaptureIPSet(ctx context.Context, source, target string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, "ipset", "save", source).Output()
	if err != nil {
		return "", fmt.Errorf("capture routing ipset: %w", err)
	}
	return writeIPSetSnapshot(string(data), source, target)
}

// CopyIPSetSnapshot reuses an already captured set for another tunnel.
func CopyIPSetSnapshot(path, source, target string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return writeIPSetSnapshot(string(data), source, target)
}

func writeIPSetSnapshot(raw, source, target string) (string, error) {
	content, err := remapIPSet(raw, source, target)
	if err != nil || content == "" {
		return "", err
	}
	file, err := os.CreateTemp("", "vpn-ipset-recovery-")
	if err != nil {
		return "", err
	}
	path := file.Name()
	_, writeErr := file.WriteString(content)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		os.Remove(path)
		return "", fmt.Errorf("write recovery ipset: %v %v", writeErr, closeErr)
	}
	return path, nil
}

func remapIPSet(raw, source, target string) (string, error) {
	var out strings.Builder
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "add" || fields[1] != source {
			continue
		}
		if net.ParseIP(fields[2]) == nil {
			if _, _, err := net.ParseCIDR(fields[2]); err != nil {
				return "", fmt.Errorf("invalid saved ipset address")
			}
		}
		fmt.Fprintf(&out, "add %s %s", target, fields[2])
		for i := 3; i+1 < len(fields); i++ {
			if fields[i] == "timeout" {
				value, err := strconv.ParseUint(fields[i+1], 10, 32)
				if err != nil {
					return "", fmt.Errorf("invalid saved ipset timeout")
				}
				fmt.Fprintf(&out, " timeout %d", value)
				break
			}
		}
		out.WriteByte('\n')
	}
	return out.String(), nil
}
