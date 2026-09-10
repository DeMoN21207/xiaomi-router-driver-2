package runtimehealth

import (
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestStatusStopsWithoutInterface(t *testing.T) {
	if got := Status("definitely-missing-interface", os.Getpid()); got != "stopped" {
		t.Fatalf("Status() = %q, want stopped", got)
	}
}

func TestProcessAliveAcceptsExternalPIDZero(t *testing.T) {
	if !ProcessAlive(0) {
		t.Fatal("ProcessAlive(0) = false, want true for externally-managed runtime")
	}
}

func TestStatusRunningForExistingInterfaceAndCurrentProcess(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatalf("Interfaces() error = %v", err)
	}

	var interfaceName string
	for _, iface := range ifaces {
		if iface.Name != "" {
			interfaceName = iface.Name
			break
		}
	}
	if interfaceName == "" {
		t.Skip("no network interfaces available")
	}

	if got := Status(interfaceName, os.Getpid()); got != "running" {
		t.Fatalf("Status(%q, current pid) = %q, want running", interfaceName, got)
	}
}

func TestProbeInterfaceReportsMissingInterface(t *testing.T) {
	ok, detail := probeInterface("definitely-missing-sb0", "1.1.1.1")

	if ok {
		t.Fatal("probeInterface() ok = true, want false")
	}
	if !strings.Contains(detail, "interface is missing") {
		t.Fatalf("detail = %q, want missing interface detail", detail)
	}
}

func TestKillIfMatchesChecksProcessIdentity(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process identity is read from Linux procfs")
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	killed, err := KillIfMatches(cmd.Process.Pid, "definitely-not-this-process")
	if err != nil {
		t.Fatal(err)
	}
	if killed {
		t.Fatal("mismatched process was killed")
	}
	if !ProcessAlive(cmd.Process.Pid) {
		t.Fatal("mismatched process did not survive identity check")
	}

	killed, err = KillIfMatches(cmd.Process.Pid, "sleep")
	if err != nil {
		t.Fatal(err)
	}
	if !killed {
		t.Fatal("matching process was not killed")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("killed process was not reaped")
	}
}
