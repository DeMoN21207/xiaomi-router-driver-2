package openvpn

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/routing"
	"xiomi-router-driver/internal/sqlitedb"
)

func TestWatchInstancePreservesRoutingOnUnexpectedExit(t *testing.T) {
	if os.Getenv("GO_WANT_OPENVPN_WATCH_HELPER") == "1" {
		os.Exit(3)
	}

	dir := t.TempDir()
	routeCalls := filepath.Join(dir, "route-calls")
	routeScript := filepath.Join(dir, "routes.sh")
	if err := os.WriteFile(routeScript, []byte("#!/bin/sh\nprintf '%s\\n' \"$1\" >> '"+routeCalls+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sqlitedb.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	manager := NewManager(dir, dir, db, routing.NewRunner(routeScript), nil)
	manager.mu.Lock()
	if err := manager.ensureReadyLocked(); err != nil {
		manager.mu.Unlock()
		t.Fatal(err)
	}
	manager.mu.Unlock()

	helperBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(helperBinary, "-test.run=TestWatchInstancePreservesRoutingOnUnexpectedExit")
	cmd.Env = append(os.Environ(), "GO_WANT_OPENVPN_WATCH_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	settings := config.DefaultRoutingSettings()
	settings.VPNIface = "tun-watch"
	instance := &managedInstance{
		ProviderID: "provider", ProviderName: "VPN", InterfaceName: settings.VPNIface,
		ProfilePath: filepath.Join(dir, "client.ovpn"), Settings: settings, PID: cmd.Process.Pid,
	}
	manager.mu.Lock()
	if err := manager.saveInstanceLocked(instance); err != nil {
		manager.mu.Unlock()
		t.Fatal(err)
	}
	manager.current[instance.ProviderID] = instance
	manager.mu.Unlock()

	manager.watchInstance(instance.ProviderID, cmd)

	if data, err := os.ReadFile(routeCalls); err == nil {
		t.Fatalf("unexpected process exit ran routing actions: %q", data)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, exists := manager.current[instance.ProviderID]; exists {
		t.Fatal("stopped runtime remained in current map")
	}
	manager.mu.Lock()
	instances, err := manager.loadInstancesLocked()
	manager.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 0 {
		t.Fatalf("stopped runtime remained persisted: %+v", instances)
	}
}
