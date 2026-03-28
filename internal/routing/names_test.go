package routing

import "testing"

func TestDomainStatsChainName(t *testing.T) {
	t.Run("short name", func(t *testing.T) {
		got := DomainStatsChainName("vpn_hosts")
		if got != "VDS_vpn_hosts" {
			t.Fatalf("unexpected chain name: %q", got)
		}
	})

	t.Run("long name stays under limit", func(t *testing.T) {
		got := DomainStatsChainName("vpn_hosts_486741aa")
		if len(got) >= 29 {
			t.Fatalf("chain name %q is too long: %d", got, len(got))
		}
		if got == LegacyDomainStatsChainName("vpn_hosts_486741aa") {
			t.Fatalf("expected shortened chain name, got legacy value %q", got)
		}
	})
}
