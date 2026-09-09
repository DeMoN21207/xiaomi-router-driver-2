package config

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"xiomi-router-driver/internal/sqlitedb"
)

func TestGroupedConfigReadPreservesOwnershipAndOrder(t *testing.T) {
	m := NewManager(openTestDB(t), "")
	state := DefaultState()
	state.Providers = []Provider{{ID: "p", Name: "VPN", Type: ProviderTypeSubscription, Source: "https://example.com/sub"}}
	state.Rules = []Rule{
		{ID: "z", Name: "First", ProviderID: "p", Domains: []string{"z.example.com", "a.example.com"}},
		{ID: "a", Name: "Second", ProviderID: "p", Domains: []string{"other.example.com"}},
		{ID: "empty", Name: "Empty", ProviderID: "p", Domains: []string{}},
	}
	state.PriorityPolicies = []PriorityPolicy{
		{ID: "z", Name: "First", ProviderID: "p", Entries: []string{"first.example.com"}, Targets: []PriorityTarget{{Location: "NL"}, {Location: "DE"}}, Schedule: []PriorityScheduleWindow{{Start: "18:00", End: "23:00", Location: "NL"}, {Start: "09:00", End: "17:00", Location: "DE"}}},
		{ID: "a", Name: "Second", ProviderID: "p", Entries: []string{"second.example.com"}, Targets: []PriorityTarget{{Location: "US"}}, Schedule: []PriorityScheduleWindow{}},
	}
	if _, err := m.Save(state); err != nil {
		t.Fatal(err)
	}
	got, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Rules, state.Rules) {
		t.Fatalf("rules changed ownership/order: %+v", got.Rules)
	}
	if !reflect.DeepEqual(got.PriorityPolicies, state.PriorityPolicies) {
		t.Fatalf("policies changed ownership/order: %+v", got.PriorityPolicies)
	}
}

func TestManagerRecoversAfterInitializationWriteFailure(t *testing.T) {
	db := openTestDB(t)
	manager := NewManager(db, "")
	if _, err := db.Exec("PRAGMA query_only = ON"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Load(); err == nil {
		t.Fatal("expected initialization write failure")
	}
	if _, err := db.Exec("PRAGMA query_only = OFF"); err != nil {
		t.Fatal(err)
	}
	state, err := manager.Load()
	if err != nil {
		t.Fatalf("initialization did not recover: %v", err)
	}
	if state.Routing.LANIface != "br-lan" {
		t.Fatalf("unexpected recovered config: %+v", state.Routing)
	}
}

func BenchmarkLoadLargeConfig(b *testing.B) {
	db, err := sqlitedb.Open(filepath.Join(b.TempDir(), "state.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	manager := NewManager(db, "")
	state := DefaultState()
	state.Providers = []Provider{{ID: "provider", Name: "Provider", Type: ProviderTypeSubscription, Source: "https://example.com/sub", Enabled: true}}
	for i := 0; i < 200; i++ {
		rule := Rule{ID: fmt.Sprintf("rule-%03d", i), Name: "Rule", ProviderID: "provider", Enabled: true}
		for j := 0; j < 20; j++ {
			rule.Domains = append(rule.Domains, fmt.Sprintf("d%d.r%d.example.com", j, i))
		}
		state.Rules = append(state.Rules, rule)
	}
	for i := 0; i < 30; i++ {
		state.PriorityPolicies = append(state.PriorityPolicies, PriorityPolicy{
			ID: fmt.Sprintf("policy-%02d", i), Name: "Policy", ProviderID: "provider", Enabled: true,
			Entries:  []string{fmt.Sprintf("p%d.example.com", i)},
			Targets:  []PriorityTarget{{Location: "NL"}, {Location: "DE"}},
			Schedule: []PriorityScheduleWindow{{Start: "09:00", End: "18:00", Location: "NL"}},
		})
	}
	if _, err := manager.Save(state); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		loaded, err := manager.Load()
		if err != nil {
			b.Fatal(err)
		}
		if len(loaded.Rules) != 200 || len(loaded.PriorityPolicies) != 30 {
			b.Fatal("incomplete config")
		}
	}
}
