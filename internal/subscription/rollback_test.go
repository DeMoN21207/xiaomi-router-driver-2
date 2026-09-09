package subscription

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/routing"
	"xiomi-router-driver/internal/runtimehealth"
)

func rollbackTestManager(t *testing.T) (*Manager, config.State, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SUBSCRIPTION_TEST_DIR", dir)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	scripts := map[string]string{
		"sing-box": "#!/bin/sh\nif [ -e \"$SUBSCRIPTION_TEST_DIR/fail-start\" ] && grep -q 'new.example.com' \"$3\"; then exit 3; fi\nexec sleep 120\n",
		"routes.sh": `#!/bin/sh
set -eu
printf '%s %s\n' "$1" "$VPN_IFACE" >> "$SUBSCRIPTION_TEST_DIR/actions"
if [ "$1" != del ]; then
  if [ -e "$SUBSCRIPTION_TEST_DIR/block" ] || [ -e "$SUBSCRIPTION_TEST_DIR/block-$VPN_IFACE" ]; then
    touch "$SUBSCRIPTION_TEST_DIR/entered"
    while [ -e "$SUBSCRIPTION_TEST_DIR/block" ] || [ -e "$SUBSCRIPTION_TEST_DIR/block-$VPN_IFACE" ]; do sleep 1; done
  fi
  if [ -e "$SUBSCRIPTION_TEST_DIR/fail-$VPN_IFACE" ] || [ -e "$SUBSCRIPTION_TEST_DIR/fail-$VPN_IFACE-$TABLE_NUM" ]; then exit 42; fi
  cp "$DOMAIN_LIST" "$SUBSCRIPTION_TEST_DIR/applied-domains"
  printf '%s\n' "$VPN_IFACE" > "$SUBSCRIPTION_TEST_DIR/active"
  printf '%s\n' "$VPN_IFACE" > "$SUBSCRIPTION_TEST_DIR/table-$TABLE_NUM"
  if [ -n "${IPSET_RESTORE_FILE:-}" ]; then
    cp "$IPSET_RESTORE_FILE" "$SUBSCRIPTION_TEST_DIR/restored-ipset"
  fi
else
  rm -f "$SUBSCRIPTION_TEST_DIR/active"
  rm -f "$SUBSCRIPTION_TEST_DIR/table-$TABLE_NUM"
  if [ -e "$SUBSCRIPTION_TEST_DIR/fail-del-$VPN_IFACE" ]; then exit 43; fi
fi
`,
		"ipset": "#!/bin/sh\nprintf 'create %s hash:net family inet timeout 300\nadd %s 203.0.113.8 timeout 120\n' \"$2\" \"$2\"\n",
	}
	for name, content := range scripts {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	previousAlive := interfaceAlive
	interfaceAlive = func(string) bool { return true }
	t.Cleanup(func() { interfaceAlive = previousAlive })
	m := NewManager(dir, dir, openSubscriptionTestDB(t), routing.NewRunner(filepath.Join(dir, "routes.sh")), nil)
	m.singBoxBinary = filepath.Join(dir, "sing-box")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = m.Cleanup(ctx)
	})
	state := config.DefaultState()
	state.Providers = []config.Provider{{ID: "provider_1", Name: "Test", Enabled: true, Type: config.ProviderTypeSubscription,
		Source: "{\n" + `"outbounds":[{"type":"vless","tag":"Old","server":"old.example.com","server_port":443},{"type":"vless","tag":"New","server":"new.example.com","server_port":443}]}`}}
	state.Rules = []config.Rule{{ID: "rule_1", ProviderID: "provider_1", Enabled: true, SelectedLocation: "Old", Domains: []string{"example.com", "203.0.113.0/24"}}}
	return m, state, dir
}

func TestApplyRestoresPreviousTunnelWhenNewRoutingFails(t *testing.T) {
	m, state, dir := rollbackTestManager(t)
	if err := m.Apply(t.Context(), state, state.Rules); err != nil {
		t.Fatal(err)
	}
	old := m.current["provider_1::old"]
	oldConfig, err := os.ReadFile(old.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	newIface := deriveRoutingSettings(state.Routing, shortHash("provider_1\nNew"), 0).VPNIface
	if err := os.WriteFile(filepath.Join(dir, "fail-"+newIface), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	state.Rules[0].SelectedLocation = "New"
	state.Rules[0].Domains = []string{"new.example.com"}
	if err := m.Apply(t.Context(), state, state.Rules); err == nil {
		t.Fatal("replacement routing failure was not reported")
	}
	m.mu.Lock()
	instances, err := m.loadInstancesLocked()
	m.mu.Unlock()
	if err != nil || len(instances) != 1 || instances[0].Location != "Old" {
		t.Fatalf("previous runtime was not restored: %+v, %v", instances, err)
	}
	if !runtimehealth.ProcessAlive(instances[0].PID) {
		t.Fatal("restored runtime is not alive")
	}
	if got, _ := os.ReadFile(old.ConfigPath); string(got) != string(oldConfig) {
		t.Fatal("rollback did not restore original outbound config")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "applied-domains")); !reflect.DeepEqual(strings.Fields(string(got)), []string{"example.com", "203.0.113.0/24"}) {
		t.Fatalf("rollback used replacement domains: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "restored-ipset")); !strings.Contains(string(got), "add "+old.Settings.IPSetName+" 203.0.113.8 timeout 120") {
		t.Fatalf("rollback lost saved destinations: %q", got)
	}
}

func TestSnapshotsRemainResponsiveDuringRoutingApply(t *testing.T) {
	m, state, dir := rollbackTestManager(t)
	if err := m.Apply(t.Context(), state, state.Rules); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Snapshots(); err != nil {
		t.Fatal(err)
	}
	block := filepath.Join(dir, "block")
	if err := os.WriteFile(block, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { finished <- m.Apply(t.Context(), state, state.Rules) }()
	t.Cleanup(func() { _ = os.Remove(block); <-finished })
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "entered")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("apply did not reach blocked routing command")
		}
		time.Sleep(5 * time.Millisecond)
	}
	response := make(chan error, 1)
	go func() { _, err := m.Snapshots(); response <- err }()
	select {
	case err := <-response:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		_ = os.Remove(block)
		<-response
		t.Fatal("panel status waited for the route apply lock")
	}
}

func TestApplyRollbackSurvivesCallerCancellation(t *testing.T) {
	m, state, dir := rollbackTestManager(t)
	if err := m.Apply(t.Context(), state, state.Rules); err != nil {
		t.Fatal(err)
	}
	newIface := deriveRoutingSettings(state.Routing, shortHash("provider_1\nNew"), 0).VPNIface
	if err := os.WriteFile(filepath.Join(dir, "block-"+newIface), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	state.Rules[0].SelectedLocation = "New"
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := m.Apply(ctx, state, state.Rules); err == nil || ctx.Err() == nil {
		t.Fatalf("expected cancelled apply, got %v (context %v)", err, ctx.Err())
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("cancelled apply or rollback exceeded its test deadline")
	}
	old := m.current["provider_1::old"]
	if old == nil || !runtimehealth.ProcessAlive(old.PID) || len(m.current) != 1 {
		t.Fatal("cancelled client prevented restoring previous VPN")
	}
}

func TestApplyRollbackOnReplacementProcessExit(t *testing.T) {
	m, state, dir := rollbackTestManager(t)
	if err := m.Apply(t.Context(), state, state.Rules); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "fail-start"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	newIface := deriveRoutingSettings(state.Routing, shortHash("provider_1\nNew"), 0).VPNIface
	interfaceAlive = func(name string) bool { return name != newIface }
	state.Rules[0].SelectedLocation = "New"
	started := time.Now()
	if err := m.Apply(t.Context(), state, state.Rules); err == nil {
		t.Fatal("exited replacement process was accepted")
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("process exit waited for the full interface timeout")
	}
	old := m.current["provider_1::old"]
	if old == nil || !runtimehealth.ProcessAlive(old.PID) || len(m.current) != 1 {
		t.Fatal("previous VPN was not restored after process exit")
	}
}

func TestApplyRollbackRestoresOriginalTablesAfterLocationRemoval(t *testing.T) {
	m, state, dir := rollbackTestManager(t)
	state.Rules = append(state.Rules, config.Rule{ID: "rule_2", ProviderID: "provider_1", Enabled: true, SelectedLocation: "New", Domains: []string{"other.example.com"}})
	if err := m.Apply(t.Context(), state, state.Rules); err != nil {
		t.Fatal(err)
	}
	oldSettings := make(map[string]config.RoutingSettings)
	for key, instance := range m.current {
		oldSettings[key] = instance.Settings
	}
	// Removing the first sorted location shifts Old from table 102 to 101.
	// Fail that new assignment, but allow its original table during rollback.
	next := deriveRoutingSettings(state.Routing, shortHash("provider_1\nOld"), 0)
	if err := os.WriteFile(filepath.Join(dir, "fail-"+next.VPNIface+"-"+strconv.Itoa(next.TableNum)), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	state.Rules = state.Rules[:1]
	if err := m.Apply(t.Context(), state, state.Rules); err == nil {
		t.Fatal("replacement routing failure was accepted")
	}
	if len(m.current) != len(oldSettings) {
		t.Fatalf("rollback restored %d of %d tunnels", len(m.current), len(oldSettings))
	}
	for key, settings := range oldSettings {
		instance := m.current[key]
		if instance == nil || instance.Settings != settings {
			t.Fatalf("original settings lost for %s", key)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "table-"+strconv.Itoa(settings.TableNum)))
		if strings.TrimSpace(string(got)) != settings.VPNIface {
			t.Fatalf("later teardown erased restored table %d: %q", settings.TableNum, got)
		}
	}
}

func TestApplyRollbackDoesNotDeleteUntouchedTableAfterStopFailure(t *testing.T) {
	m, state, dir := rollbackTestManager(t)
	state.Rules = append(state.Rules, config.Rule{ID: "rule_2", ProviderID: "provider_1", Enabled: true, SelectedLocation: "New", Domains: []string{"other.example.com"}})
	if err := m.Apply(t.Context(), state, state.Rules); err != nil {
		t.Fatal(err)
	}
	untouched := m.current["provider_1::old"]
	first := m.current["provider_1::new"]
	if err := os.WriteFile(filepath.Join(dir, "fail-del-"+first.Settings.VPNIface), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	state.Providers[0].Source = strings.Replace(state.Providers[0].Source, `"outbounds":[`, `"outbounds":[{"type":"vless","tag":"AAA","server":"aaa.example.com","server_port":443},`, 1)
	state.Rules = append(state.Rules, config.Rule{ID: "rule_3", ProviderID: "provider_1", Enabled: true, SelectedLocation: "AAA", Domains: []string{"added.example.com"}})
	if err := m.Apply(t.Context(), state, state.Rules); err == nil {
		t.Fatal("partial stop failure was accepted")
	}
	if kept := m.current[untouched.Key]; kept == nil || kept.PID != untouched.PID {
		t.Fatal("rollback restarted an untouched VPN")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "table-"+strconv.Itoa(untouched.Settings.TableNum)))
	if strings.TrimSpace(string(got)) != untouched.Settings.VPNIface {
		t.Fatalf("rollback deleted untouched VPN table: %q", got)
	}
}

func TestApplyAlreadyCancelledLeavesCurrentProcessUntouched(t *testing.T) {
	m, state, _ := rollbackTestManager(t)
	if err := m.Apply(t.Context(), state, state.Rules); err != nil {
		t.Fatal(err)
	}
	old := m.current["provider_1::old"]
	state.Rules[0].SelectedLocation = "New"
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := m.Apply(ctx, state, state.Rules); err == nil {
		t.Fatal("cancelled apply was accepted")
	}
	if kept := m.current[old.Key]; kept == nil || kept.PID != old.PID {
		t.Fatal("cancelled request restarted an existing VPN")
	}
}
