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

func TestDomainStatsMaxDomainsWithFallback(t *testing.T) {
	t.Setenv("VPN_MANAGER_DOMAIN_STATS_MAX_DOMAINS", "")
	t.Setenv("DOMAIN_STATS_MAX_DOMAINS", "")

	if got := DomainStatsMaxDomainsWithFallback(96); got != 96 {
		t.Fatalf("expected fallback 96, got %d", got)
	}
}

func TestDomainStatsEnabled(t *testing.T) {
	t.Setenv("VPN_MANAGER_DOMAIN_STATS_MAX_DOMAINS", "2")
	t.Setenv("DOMAIN_STATS_MAX_DOMAINS", "")
	t.Setenv("VPN_MANAGER_DOMAIN_STATS", "")
	t.Setenv("DOMAIN_STATS", "")

	if !DomainStatsEnabled(2) {
		t.Fatalf("expected domain stats enabled for count at limit")
	}
	if DomainStatsEnabled(3) {
		t.Fatalf("expected domain stats disabled above limit")
	}
}

func TestDomainStatsEnabledModeOverrides(t *testing.T) {
	t.Run("explicit off disables stats regardless of count", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DOMAIN_STATS", "off")
		t.Setenv("DOMAIN_STATS", "")
		t.Setenv("VPN_MANAGER_DOMAIN_STATS_MAX_DOMAINS", "512")
		t.Setenv("DOMAIN_STATS_MAX_DOMAINS", "")

		if DomainStatsEnabled(1) {
			t.Fatalf("expected domain stats disabled when mode is off")
		}
	})

	t.Run("explicit on enables stats above auto threshold", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DOMAIN_STATS", "on")
		t.Setenv("DOMAIN_STATS", "")
		t.Setenv("VPN_MANAGER_DOMAIN_STATS_MAX_DOMAINS", "1")
		t.Setenv("DOMAIN_STATS_MAX_DOMAINS", "")
		t.Setenv("VPN_MANAGER_ALLOW_HEAVY_DOMAIN_STATS", "1")

		if !DomainStatsEnabled(200) {
			t.Fatalf("expected domain stats enabled when mode is on")
		}
	})

	t.Run("hard cap disables detailed stats without advanced override", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DOMAIN_STATS", "on")
		t.Setenv("DOMAIN_STATS", "")
		t.Setenv("VPN_MANAGER_DOMAIN_STATS_MAX_DOMAINS", "512")
		t.Setenv("DOMAIN_STATS_MAX_DOMAINS", "")
		t.Setenv("VPN_MANAGER_ALLOW_HEAVY_DOMAIN_STATS", "")

		if DomainStatsEnabled(200) {
			t.Fatalf("expected hard cap to disable heavy domain stats above limit")
		}
	})
}

func TestDomainStatsModeWithFallback(t *testing.T) {
	t.Run("env wins over fallback", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DOMAIN_STATS", "off")
		t.Setenv("DOMAIN_STATS", "")

		if got := DomainStatsModeWithFallback("on"); got != "off" {
			t.Fatalf("expected off, got %q", got)
		}
	})

	t.Run("fallback normalizes auto", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DOMAIN_STATS", "")
		t.Setenv("DOMAIN_STATS", "")

		if got := DomainStatsModeWithFallback("auto"); got != "auto" {
			t.Fatalf("expected auto, got %q", got)
		}
	})
}
