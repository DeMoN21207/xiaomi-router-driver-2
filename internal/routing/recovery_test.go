package routing

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"xiomi-router-driver/internal/config"
)

func TestRemapIPSetKeepsAddressesAndRemainingTimeout(t *testing.T) {
	input := "create old hash:net family inet timeout 86400\nadd old 1.2.3.4 timeout 120\nadd old 10.0.0.0/8 timeout 0\nadd other 8.8.8.8 timeout 90\n"
	got, err := remapIPSet(input, "old", "backup")
	if err != nil {
		t.Fatal(err)
	}
	want := "add backup 1.2.3.4 timeout 120\nadd backup 10.0.0.0/8 timeout 0\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFastRecoveryRestoresSavedIPSetWithoutDNS(t *testing.T) {
	dir := t.TempDir()
	seed := filepath.Join(dir, "seed")
	if err := os.WriteFile(seed, []byte("add backup 1.2.3.4 timeout 60\n"), 0600); err != nil {
		t.Fatal(err)
	}
	script := string(embeddedScript)
	start := strings.Index(script, "prime_ipsets() {")
	end := strings.Index(script[start:], "\nis_ipv4_entry()") + start
	body := script[start:end]
	command := `ipset() { echo "$*"; cat; }
prime_domain_ips() { echo UNEXPECTED_DNS; exit 1; }
PRIME_MAX_DOMAINS=0
` + body + "\nprime_ipsets\n"
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "IPSET_RESTORE_FILE="+seed)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "add backup 1.2.3.4 timeout 60") || strings.Contains(string(out), "UNEXPECTED_DNS") {
		t.Fatalf("restore output=%s err=%v", out, err)
	}
}

func TestRoutingCancellationStopsChildCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX process groups")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "late-mutation")
	script := filepath.Join(dir, "routes.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n(sleep 2; touch \"$LATE_MUTATION\") &\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LATE_MUTATION", marker)
	t.Setenv("VPN_SCRIPT_SHELL", "sh")
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := NewRunner(script).Run(ctx, "sync", config.DefaultRoutingSettings())
	if err == nil {
		t.Fatal("cancelled command succeeded")
	}
	if time.Since(start) > 1500*time.Millisecond {
		t.Fatal("child kept cancelled routing command alive")
	}
	time.Sleep(2 * time.Second)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("routing child mutated state after cancellation")
	}
}
