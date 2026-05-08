package status

import (
	"testing"
	"time"

	"xiomi-router-driver/internal/config"
)

func TestDomainTrafficStoreListAppliesSortAndLimitInQuery(t *testing.T) {
	store := newDomainTrafficStore(openSiteTrafficTestDB(t))
	now := time.Date(2026, time.March, 29, 10, 0, 0, 0, time.UTC).Format(time.RFC3339)

	if err := store.Upsert([]DomainTrafficStat{
		{Domain: "alpha.com", Bytes: 120, Packets: 3},
		{Domain: "beta.com", Bytes: 420, Packets: 8},
		{Domain: "gamma.com", Bytes: 240, Packets: 5},
	}, now); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	byBytes, err := store.List("bytes", 2)
	if err != nil {
		t.Fatalf("List(bytes, 2) error = %v", err)
	}
	if byBytes.TotalBytes != 780 {
		t.Fatalf("expected total bytes 780, got %d", byBytes.TotalBytes)
	}
	if byBytes.UpdatedAt != now {
		t.Fatalf("expected updatedAt %q, got %q", now, byBytes.UpdatedAt)
	}
	if len(byBytes.Stats) != 2 {
		t.Fatalf("expected 2 stats, got %d", len(byBytes.Stats))
	}
	if byBytes.Stats[0].Domain != "beta.com" || byBytes.Stats[1].Domain != "gamma.com" {
		t.Fatalf("unexpected byte order: %+v", byBytes.Stats)
	}

	byDomain, err := store.List("domain", 0)
	if err != nil {
		t.Fatalf("List(domain, 0) error = %v", err)
	}
	if len(byDomain.Stats) != 3 {
		t.Fatalf("expected 3 stats, got %d", len(byDomain.Stats))
	}
	if byDomain.Stats[0].Domain != "alpha.com" || byDomain.Stats[1].Domain != "beta.com" || byDomain.Stats[2].Domain != "gamma.com" {
		t.Fatalf("unexpected domain order: %+v", byDomain.Stats)
	}
}

func TestAggregateLiveDomainTrafficMergesDirectionsAndSorts(t *testing.T) {
	sampledAt := time.Date(2026, time.March, 29, 10, 30, 0, 0, time.UTC).Format(time.RFC3339)
	stats, totalBytes := aggregateLiveDomainTraffic([]domainTrafficChainSample{
		{
			Chain: "VDS_test",
			Stats: []DomainTrafficStat{
				{Domain: "chatgpt.com|up", Bytes: 120, Packets: 2},
				{Domain: "chatgpt.com|dn", Bytes: 380, Packets: 7},
				{Domain: "1.1.1.1|dn", Bytes: 80, Packets: 1},
				{Domain: "10.10.0.0/16|up", Bytes: 0, Packets: 0},
				{Domain: "unused.example|up", Bytes: 0, Packets: 0},
			},
		},
	}, sampledAt)

	if totalBytes != 580 {
		t.Fatalf("expected total bytes 580, got %d", totalBytes)
	}

	sortDomainTrafficStats(stats, "bytes")
	if len(stats) != 4 {
		t.Fatalf("expected 4 stats, got %d", len(stats))
	}
	if stats[0].Domain != "chatgpt.com" {
		t.Fatalf("expected chatgpt.com first, got %+v", stats)
	}
	if stats[0].Bytes != 500 || stats[0].TXBytes != 120 || stats[0].RXBytes != 380 || stats[0].Packets != 9 {
		t.Fatalf("unexpected merged chatgpt.com stats: %+v", stats[0])
	}
	if stats[0].UpdatedAt != sampledAt {
		t.Fatalf("expected updatedAt %q, got %q", sampledAt, stats[0].UpdatedAt)
	}

	sortDomainTrafficStats(stats, "domain")
	if stats[0].Domain != "1.1.1.1" || stats[1].Domain != "10.10.0.0/16" || stats[2].Domain != "chatgpt.com" {
		t.Fatalf("unexpected domain order: %+v", stats)
	}
}

func TestStateHasActiveOpenVPNDomainRulesIgnoresSubscriptionOnlyRules(t *testing.T) {
	state := config.State{
		Providers: []config.Provider{
			{ID: "sub", Type: config.ProviderTypeSubscription, Enabled: true},
			{ID: "ovpn", Type: config.ProviderTypeOpenVPN, Enabled: true},
		},
		Rules: []config.Rule{
			{ProviderID: "sub", Enabled: true, Domains: []string{"chatgpt.com"}},
		},
	}

	if stateHasActiveOpenVPNDomainRules(state) {
		t.Fatalf("expected subscription-only routing not to require the base OpenVPN stats chain")
	}

	state.Rules = append(state.Rules, config.Rule{ProviderID: "ovpn", Enabled: true, Domains: []string{"example.com"}})
	if !stateHasActiveOpenVPNDomainRules(state) {
		t.Fatalf("expected active OpenVPN rule to require the base stats chain")
	}
}
