package subscription

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRollbackMigratesCompleteLegacyDomainFragment(t *testing.T) {
	dir := t.TempDir()
	instance := &managedInstance{ConfigPath: filepath.Join(dir, "old.json"), DomainCount: 2}
	instance.Settings.DNSMasqConfigFile = filepath.Join(dir, "dnsmasq.conf")
	instance.Settings.IPSetName = "old_set"
	fragment := "ipset=/example.com/old_set,stats_set\nipset=/other.example/old_set\nipset=/unrelated.example/different_set\n"
	if err := os.WriteFile(instance.Settings.DNSMasqConfigFile, []byte(fragment), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := rollbackDomains(t.Context(), instance, 2)
	if err != nil || !reflect.DeepEqual(got, []string{"example.com", "other.example"}) {
		t.Fatalf("legacy domain-only migration = %v, %v", got, err)
	}
	instance.DomainCount = 3
	if _, err := rollbackDomains(t.Context(), instance, 2); err == nil {
		t.Fatal("incomplete multi-tunnel legacy list was accepted")
	}
	previous := []string{"example.com", "other.example", "203.0.113.0/24"}
	got, err = rollbackDomains(WithPreviousDomains(t.Context(), previous), instance, 1)
	if err != nil || !reflect.DeepEqual(got, previous) {
		t.Fatalf("single legacy runtime lost literal IP entries: %v, %v", got, err)
	}
	if _, err := rollbackDomains(WithPreviousDomains(t.Context(), []string{"wrong.example", "other.example", "203.0.113.0/24"}), instance, 1); err == nil {
		t.Fatal("a same-sized list belonging to another route was accepted")
	}
}

func TestActiveDomainSidecarSurvivesPruning(t *testing.T) {
	m, state, _ := rollbackTestManager(t)
	if err := m.Apply(t.Context(), state, state.Rules); err != nil {
		t.Fatal(err)
	}
	instance := m.current["provider_1::old"]
	m.mu.Lock()
	err := m.pruneRuntimeFilesLocked()
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	got, err := rollbackDomains(t.Context(), instance, 2)
	if err != nil || !reflect.DeepEqual(got, state.Rules[0].Domains) {
		t.Fatalf("active domain list was lost during pruning: %v, %v", got, err)
	}
}
