package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/routing"
	"xiomi-router-driver/internal/status"
	"xiomi-router-driver/internal/subscription"
)

// Opt-in test for a deployed router. Temporary standby listeners do not alter
// routing or stop the active VPN. Credentials remain on the router.
func TestRouterHTTPSProbesIntegration(t *testing.T) {
	root := os.Getenv("VPN_MANAGER_TEST_ROOT")
	if root == "" {
		t.Skip("set VPN_MANAGER_TEST_ROOT on the router to opt in")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	read := func(path string, out any) {
		response, err := client.Get("http://127.0.0.1:18080" + path)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if err := json.NewDecoder(response.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	var state config.State
	var snapshot status.Snapshot
	read("/api/config", &state)
	read("/api/status", &snapshot)
	s := &Supervisor{}
	for _, item := range snapshot.SubscriptionRuntime {
		if item.Status != "running" {
			continue
		}
		result := s.subscriptionTunnelProbe(t.Context(), item.InterfaceName, item.FWMark)
		if !result.Healthy {
			t.Fatalf("active tunnel %s: %s", item.Location, result.Detail)
		}
		t.Logf("active %s: %s", item.Location, result.Detail)
	}
	for _, policy := range state.PriorityPolicies {
		provider, ok := findProvider(state.Providers, policy.ProviderID)
		if !ok || !policy.Enabled {
			continue
		}
		entries, err := subscription.LoadCachedEntries(provider.Source, filepath.Join(root, "data", ".vpn-manager", "subscriptions"))
		if err != nil {
			t.Fatal("could not read cached endpoints")
		}
		for _, target := range policy.Targets {
			entry, ok := findSubscriptionEntry(entries, target.Location)
			if !ok {
				t.Fatalf("missing target %s", target.Location)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			result := probeStandbyEntry(ctx, filepath.Join(root, "bin", "sing-box"), entry)
			cancel()
			if !result.Healthy {
				t.Errorf("standby %s: %s", target.Location, result.Detail)
			}
			t.Logf("standby %s: healthy=%t latency=%dms", target.Location, result.Healthy, result.LatencyMs)
		}
	}
}

func TestRouterIPSetRecoveryIntegration(t *testing.T) {
	if os.Getenv("VPN_MANAGER_TEST_ROOT") == "" {
		t.Skip("router integration is opt in")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	response, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://127.0.0.1:18080/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var snapshot status.Snapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.SubscriptionRuntime) != 1 {
		t.Fatal("expected one active subscription runtime")
	}
	target := fmt.Sprintf("vpn_recovery_test_%d", os.Getpid())
	path, err := routing.CaptureIPSet(ctx, snapshot.SubscriptionRuntime[0].IPSetName, target)
	if err != nil || path == "" {
		t.Fatalf("capture failed: %v", err)
	}
	defer os.Remove(path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := exec.CommandContext(ctx, "ipset", "create", target, "hash:net", "family", "inet", "timeout", "86400").Run(); err != nil {
		t.Fatal(err)
	}
	defer exec.Command("ipset", "destroy", target).Run()
	cmd := exec.CommandContext(ctx, "ipset", "restore", "-exist")
	cmd.Stdin = strings.NewReader(string(data))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restore failed: %v %s", err, out)
	}
	out, err := exec.CommandContext(ctx, "ipset", "save", target).Output()
	if err != nil {
		t.Fatal(err)
	}
	want, got := strings.Count(string(data), "add "), strings.Count(string(out), "add ")
	if want == 0 || got != want {
		t.Fatalf("copied %d of %d destinations", got, want)
	}
	t.Logf("copied %d destinations into an unreferenced temporary ipset; active routes unchanged", got)
}
