package subscription

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/routing"
)

func TestWatchInstanceTeardownRoutingOnUnexpectedExit(t *testing.T) {
	if os.Getenv("GO_WANT_SUBSCRIPTION_WATCH_HELPER") == "1" {
		os.Exit(3)
	}

	tempDir := t.TempDir()
	db := openSubscriptionTestDB(t)
	manager := NewManager(tempDir, tempDir, db, nil, nil)

	manager.mu.Lock()
	if err := manager.ensureReadyLocked(); err != nil {
		manager.mu.Unlock()
		t.Fatalf("ensureReadyLocked() error = %v", err)
	}
	manager.mu.Unlock()

	configPath := filepath.Join(manager.runtimeDir, "watch-test.json")
	domainListPath := filepath.Join(manager.runtimeDir, "watch-test.domains.list")
	if err := os.WriteFile(configPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	if err := os.WriteFile(domainListPath, []byte("example.com\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(domain list) error = %v", err)
	}

	helperBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("Executable() error = %v", err)
	}
	cmd := exec.Command(helperBinary, "-test.run=TestWatchInstanceTeardownRoutingOnUnexpectedExit")
	cmd.Env = append(os.Environ(), "GO_WANT_SUBSCRIPTION_WATCH_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	settings := config.DefaultRoutingSettings()
	settings.VPNIface = "sbnl123456"

	instance := &managedInstance{
		Key:            "provider_1::nl",
		ProviderID:     "provider_1",
		ProviderName:   "FizzVPN",
		Location:       "NL",
		InterfaceName:  settings.VPNIface,
		DomainCount:    1,
		ConfigPath:     configPath,
		Settings:       settings,
		PID:            cmd.Process.Pid,
		domainListPath: domainListPath,
	}

	manager.mu.Lock()
	if err := manager.saveInstanceLocked(instance); err != nil {
		manager.mu.Unlock()
		t.Fatalf("saveInstanceLocked() error = %v", err)
	}
	manager.current[instance.Key] = instance
	manager.mu.Unlock()

	original := runSubscriptionRoutingTeardown
	t.Cleanup(func() {
		runSubscriptionRoutingTeardown = original
	})

	var (
		called      bool
		gotSettings config.RoutingSettings
	)
	runSubscriptionRoutingTeardown = func(_ *routing.Runner, _ context.Context, settings config.RoutingSettings) error {
		called = true
		gotSettings = settings
		return nil
	}

	manager.watchInstance(instance.Key, cmd)

	if !called {
		t.Fatalf("expected routing teardown to be called")
	}
	if gotSettings != settings {
		t.Fatalf("routing teardown settings = %+v, want %+v", gotSettings, settings)
	}
	if _, exists := manager.current[instance.Key]; exists {
		t.Fatalf("expected runtime to be removed from current map")
	}

	manager.mu.Lock()
	instances, err := manager.loadInstancesLocked()
	manager.mu.Unlock()
	if err != nil {
		t.Fatalf("loadInstancesLocked() error = %v", err)
	}
	if len(instances) != 0 {
		t.Fatalf("expected persisted runtime to be deleted, got %+v", instances)
	}

	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("expected config to be pruned, err=%v", err)
	}
	if _, err := os.Stat(domainListPath); !os.IsNotExist(err) {
		t.Fatalf("expected domain list to be pruned, err=%v", err)
	}
}
