package openvpn

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/routing"
	"xiomi-router-driver/internal/runtimehealth"
	"xiomi-router-driver/internal/sqlitedb"
)

func TestOpenVPNProcessSurvivesCompletedApplyContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix child process")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "openvpn")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf 'ready\\n'\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, stdout, stderr, err := startProcess(ctx, binary, filepath.Join(dir, "client.ovpn"), "tun9")
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	defer stderr.Close()
	defer cmd.Process.Kill()
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("child did not become ready: %q / %v", line, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	cancel()
	select {
	case err := <-done:
		t.Fatalf("finishing an apply killed the long-lived VPN process: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("explicit cleanup did not stop the process")
	}
}

func TestOpenVPNProcessRejectsAlreadyCanceledStart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, err := startProcess(ctx, os.Args[0], filepath.Join(t.TempDir(), "client.ovpn"), "tun9")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("start error = %v, want canceled", err)
	}
}

func TestInterfaceWaitHonorsApplyDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	err := waitForInterface(ctx, "missing-test-vpn", 300*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("interface wait ignored apply deadline: %v", err)
	}
}

func TestCanceledStartupStopsProcessEvenWhenDatabaseFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix child process")
	}
	dir := t.TempDir()
	binary, profile, script := filepath.Join(dir, "openvpn"), filepath.Join(dir, "client.ovpn"), filepath.Join(dir, "routes.sh")
	for name, body := range map[string]string{binary: "#!/bin/sh\nexec sleep 60\n", profile: "client\n", script: "#!/bin/sh\nexit 0\n"} {
		if err := os.WriteFile(name, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("VPN_MANAGER_OPENVPN_BIN", binary)
	db, err := sqlitedb.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	manager := NewManager(dir, dir, db, routing.NewRunner(script), nil)
	if _, err := manager.Snapshots(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	settings := config.DefaultRoutingSettings()
	settings.VPNIface = "missing-test-vpn"
	go func() {
		done <- manager.Apply(ctx, config.Provider{ID: "vpn", Name: "VPN", Source: profile}, []string{"example.com"}, settings)
	}()
	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := db.QueryRow("SELECT pid FROM openvpn_runtime_instances WHERE provider_id = 'vpn'").Scan(&pid); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("process was not persisted during startup")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Kill()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("apply error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled startup did not return")
	}
	deadline = time.Now().Add(time.Second)
	for runtimehealth.ProcessAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if runtimehealth.ProcessAlive(pid) {
		t.Fatal("database failure left the canceled startup process alive")
	}
}
