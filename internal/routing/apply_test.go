package routing

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xiomi-router-driver/internal/config"
)

func TestRunnerPassesRouteRefreshTuningToScript(t *testing.T) {
	tempDir := t.TempDir()
	outputPath := filepath.Join(tempDir, "env.out")
	scriptPath := filepath.Join(tempDir, "update_routes.sh")
	script := `#!/bin/sh
{
  echo "ACTION=$1"
  echo "PRIME_MAX_DOMAINS=$PRIME_MAX_DOMAINS"
  echo "IPSET_TIMEOUT=$IPSET_TIMEOUT"
  echo "CONNTRACK_FLUSH_ON_APPLY=$CONNTRACK_FLUSH_ON_APPLY"
  echo "DNS_PROXY_SERVER=$DNS_PROXY_SERVER"
  echo "DOMAIN_LIST=$DOMAIN_LIST"
} > "$OUT_FILE"
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Setenv("OUT_FILE", outputPath)
	t.Setenv("VPN_SCRIPT_SHELL", "sh")
	t.Setenv("VPN_MANAGER_PRIME_MAX_DOMAINS", "")
	t.Setenv("PRIME_MAX_DOMAINS", "")
	t.Setenv("VPN_MANAGER_IPSET_TIMEOUT", "")
	t.Setenv("IPSET_TIMEOUT", "")
	t.Setenv("VPN_MANAGER_CONNTRACK_FLUSH_ON_APPLY", "")
	t.Setenv("CONNTRACK_FLUSH_ON_APPLY", "")

	state := config.DefaultState()
	state.Routing.LoadProfile = config.RoutingLoadProfileDetailed

	runner := NewRunner(scriptPath)
	runner.SetDNSProxyServer("127.0.0.1#15353")
	if err := runner.RunWithOptions(context.Background(), "sync", RunOptions{
		Settings:       state.Routing,
		DomainListPath: "domains.list",
	}); err != nil {
		t.Fatalf("RunWithOptions() error = %v", err)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	output := string(data)
	for _, want := range []string{
		"ACTION=sync",
		"PRIME_MAX_DOMAINS=512",
		"IPSET_TIMEOUT=86400",
		"CONNTRACK_FLUSH_ON_APPLY=1",
		"DNS_PROXY_SERVER=127.0.0.1#15353",
		"DOMAIN_LIST=domains.list",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, output)
		}
	}
}
