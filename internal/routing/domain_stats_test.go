package routing

import "testing"

func TestDomainStatsMaxDomainsDefaultsAndOverrides(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DOMAIN_STATS_MAX_DOMAINS", "")
		t.Setenv("DOMAIN_STATS_MAX_DOMAINS", "")

		if got := DomainStatsMaxDomains(); got != defaultDomainStatsMaxDomains {
			t.Fatalf("expected default %d, got %d", defaultDomainStatsMaxDomains, got)
		}
	})

	t.Run("vpn manager env overrides generic env", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DOMAIN_STATS_MAX_DOMAINS", "128")
		t.Setenv("DOMAIN_STATS_MAX_DOMAINS", "512")

		if got := DomainStatsMaxDomains(); got != 128 {
			t.Fatalf("expected 128, got %d", got)
		}
	})

	t.Run("generic env fallback", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DOMAIN_STATS_MAX_DOMAINS", "")
		t.Setenv("DOMAIN_STATS_MAX_DOMAINS", "512")

		if got := DomainStatsMaxDomains(); got != 512 {
			t.Fatalf("expected 512, got %d", got)
		}
	})
}

func TestDomainStatsEnabled(t *testing.T) {
	t.Setenv("VPN_MANAGER_DOMAIN_STATS_MAX_DOMAINS", "2")
	t.Setenv("DOMAIN_STATS_MAX_DOMAINS", "")

	if !DomainStatsEnabled(2) {
		t.Fatalf("expected domain stats enabled for count at limit")
	}
	if DomainStatsEnabled(3) {
		t.Fatalf("expected domain stats disabled above limit")
	}
}
