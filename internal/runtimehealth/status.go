package runtimehealth

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	defaultProbeHost    = "1.1.1.1"
	defaultProbeTimeout = 1500 * time.Millisecond
)

type Check struct {
	InterfaceName        string
	PID                  int
	ProcessMarkers       []string
	ProbeHost            string
	EnableInterfaceProbe bool
}

type Assessment struct {
	Status string
	Detail string
}

// Assess reports a runtime as running only when its interface exists, the
// managed process still matches the expected command markers, and the tunnel
// responds to a single ping through that interface when probing is enabled.
func Assess(check Check) Assessment {
	if !InterfaceAlive(check.InterfaceName) {
		return Assessment{Status: "stopped", Detail: "interface is missing"}
	}
	if !ProcessAlive(check.PID, check.ProcessMarkers...) {
		return Assessment{Status: "stopped", Detail: "managed process is not running"}
	}
	if check.EnableInterfaceProbe && runtime.GOOS == "linux" {
		ok, detail := probeInterface(check.InterfaceName, firstNonEmpty(strings.TrimSpace(check.ProbeHost), resolveProbeHost()))
		if !ok {
			return Assessment{Status: "stopped", Detail: detail}
		}
	}
	return Assessment{Status: "running"}
}

// Status preserves the lightweight interface/process-only status check for
// places that don't have enough context for a full runtime assessment.
func Status(interfaceName string, pid int) string {
	if !InterfaceAlive(interfaceName) {
		return "stopped"
	}
	if !ProcessAlive(pid) {
		return "stopped"
	}
	return "running"
}

func InterfaceAlive(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}

	_, err := net.InterfaceByName(name)
	return err == nil
}

func ProcessAlive(pid int, markers ...string) bool {
	// pid<=0 means the runtime is attached to an externally-managed interface
	// and we can only rely on the interface presence.
	if pid <= 0 {
		return true
	}

	// The router target is Linux. On Windows dev hosts signal probing is not
	// portable, so keep the previous "assume alive" behavior there.
	if runtime.GOOS == "windows" {
		return true
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	err = process.Signal(syscall.Signal(0))
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}

	if len(markers) == 0 {
		return true
	}

	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}

	normalizedCmdline := strings.ToLower(strings.ReplaceAll(string(cmdline), "\x00", " "))
	for _, marker := range markers {
		marker = normalizeMarker(marker)
		if marker == "" {
			continue
		}
		if !strings.Contains(normalizedCmdline, marker) {
			return false
		}
	}

	return true
}

func probeInterface(interfaceName string, host string) (bool, string) {
	interfaceName = strings.TrimSpace(interfaceName)
	host = strings.TrimSpace(host)
	if interfaceName == "" || host == "" {
		return true, ""
	}

	pingBinary, err := exec.LookPath("ping")
	if err != nil {
		return true, ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultProbeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, pingBinary, "-c", "1", "-W", "1", "-I", interfaceName, host)
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return false, fmt.Sprintf("tunnel probe timeout via %s", interfaceName)
	}
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return false, fmt.Sprintf("tunnel probe failed via %s: %s", interfaceName, message)
	}

	return true, ""
}

func normalizeMarker(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return strings.ToLower(filepath.Base(value))
}

func resolveProbeHost() string {
	return firstNonEmpty(
		strings.TrimSpace(os.Getenv("VPN_MANAGER_RUNTIME_PROBE_HOST")),
		strings.TrimSpace(os.Getenv("VPN_MANAGER_WAN_PROBE")),
		defaultProbeHost,
	)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
