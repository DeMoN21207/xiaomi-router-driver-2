package automation

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/sqlitedb"
	"xiomi-router-driver/internal/status"
	"xiomi-router-driver/internal/subscription"
)

func TestManualPriorityLoadsRuntimeAfterPreviousApplyCompletes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local WAN probe fixture uses a Unix executable")
	}
	s, state, _ := priorityConcurrencySupervisor(t)
	fixtureDir := t.TempDir()
	// Use an existing no-op executable; no shell or network probe participates
	// in this test, including the asynchronous WAN cache initialization.
	trueBinary, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(trueBinary, filepath.Join(fixtureDir, "ping")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fixtureDir)
	t.Setenv("VPN_MANAGER_WAN_CACHE_TTL_MS", "3600000")
	db, err := sqlitedb.Open(filepath.Join(fixtureDir, "runtime.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	subscriptions := subscription.NewManager(fixtureDir, fixtureDir, db, nil, nil)
	if _, err := subscriptions.Snapshots(); err != nil {
		t.Fatal(err)
	}
	interfaces, err := net.Interfaces()
	if err != nil || len(interfaces) == 0 {
		t.Fatalf("list local interfaces: %v", err)
	}
	// A PID of zero models an externally managed interface, requiring no child
	// VPN process or changes to the host's networking.
	publishRuntime := func(location string) {
		t.Helper()
		if _, err := db.Exec(`DELETE FROM subscription_runtime_instances`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO subscription_runtime_instances
			(key, provider_id, provider_name, location, interface_name, domain_count, config_path, settings_json, pid)
			VALUES (?, 'provider_1', 'Sub', ?, ?, 0, '', '{}', 0)`,
			"provider_1::"+strings.ToLower(location), location, interfaces[0].Name); err != nil {
			t.Fatal(err)
		}
	}
	publishRuntime("Germany")
	s.status = status.NewService(s.state, nil, nil, subscriptions, "", fixtureDir, fixtureDir, db, "")
	snapshot := waitForPriorityWANFixture(t, s.status)
	if len(snapshot.SubscriptionRuntime) != 1 || snapshot.SubscriptionRuntime[0].Status != "running" {
		t.Fatalf("initial runtime fixture is not running: %+v", snapshot.SubscriptionRuntime)
	}
	if err := s.SetPriorityOverride("policy_1", "Netherlands"); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var applyCount atomic.Int32
	s.applyState = func(context.Context, config.State) error {
		applyCount.Add(1)
		started <- struct{}{}
		<-release
		return nil
	}
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		s.maybePriorityPolicies(t.Context(), state, snapshot)
	}()
	var releaseOnce sync.Once
	defer func() {
		releaseOnce.Do(func() { close(release) })
		priorityAwait(t, firstDone)
	}()
	priorityAwait(t, started)
	manualStarted := make(chan struct{})
	manualDone := make(chan struct{})
	var manualErr error
	go func() {
		defer close(manualDone)
		close(manualStarted)
		manualErr = s.ApplyPriorityPolicies(t.Context())
	}()
	defer func() {
		releaseOnce.Do(func() { close(release) })
		priorityAwait(t, manualDone)
	}()
	priorityAwait(t, manualStarted)
	select {
	case <-manualDone:
		t.Fatal("manual application completed while the previous apply was still running")
	case <-time.After(100 * time.Millisecond):
	}

	// The in-flight apply replaces Germany with Netherlands before releasing
	// serialization. The waiting request must evaluate this new runtime.
	publishRuntime("Netherlands")
	releaseOnce.Do(func() { close(release) })
	priorityAwait(t, firstDone)
	priorityAwait(t, manualDone)
	if manualErr != nil {
		t.Fatal(manualErr)
	}
	if got := applyCount.Load(); got != 1 {
		t.Fatalf("waiting request used the old runtime and redundantly applied routes: calls=%d, want 1", got)
	}
	current := priorityStatusPromptly(t, s)
	for _, target := range current.Policies[0].Targets {
		if target.Location == "Netherlands" && (target.ConsecutiveFailures != 0 || target.LastError != "") {
			t.Fatalf("waiting request marked the new active runtime unavailable: %+v", target)
		}
	}
}

func waitForPriorityWANFixture(t *testing.T, service *status.Service) status.Snapshot {
	t.Helper()
	type fixtureResult struct {
		snapshot status.Snapshot
		err      error
	}
	ready := make(chan fixtureResult, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			snapshot, err := service.RuntimeSnapshot(ctx)
			if err != nil || snapshot.WAN.State != "checking" {
				if err == nil && snapshot.WAN.State != "up" {
					err = fmt.Errorf("local WAN fixture failed: %+v", snapshot.WAN)
				}
				ready <- fixtureResult{snapshot: snapshot, err: err}
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	// Await the actual published result, subject to the test's own cancellation.
	// A one-second startup guess flakes when other race-enabled packages build.
	select {
	case result := <-ready:
		if result.err != nil {
			t.Fatal(result.err)
		}
		return result.snapshot
	case <-ctx.Done():
		t.Fatalf("waiting for local WAN fixture: %v", ctx.Err())
		return status.Snapshot{}
	}
}
