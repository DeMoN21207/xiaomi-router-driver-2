package status

import (
	"testing"
	"time"
)

func TestResolveDomainTrafficSampleInterval(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DOMAIN_TRAFFIC_SAMPLE_INTERVAL", "")
		if got := resolveDomainTrafficSampleInterval(); got != defaultDomainTrafficSampleInterval {
			t.Fatalf("expected default %v, got %v", defaultDomainTrafficSampleInterval, got)
		}
	})

	t.Run("allow off", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DOMAIN_TRAFFIC_SAMPLE_INTERVAL", "off")
		if got := resolveDomainTrafficSampleInterval(); got != 0 {
			t.Fatalf("expected disabled interval, got %v", got)
		}
	})

	t.Run("accept duration", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DOMAIN_TRAFFIC_SAMPLE_INTERVAL", "120s")
		if got := resolveDomainTrafficSampleInterval(); got != 120*time.Second {
			t.Fatalf("expected 120s, got %v", got)
		}
	})
}

func TestResolveSiteTrafficSampleInterval(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_SITE_TRAFFIC_SAMPLE_INTERVAL", "")
		if got := resolveSiteTrafficSampleInterval(); got != defaultSiteTrafficSampleInterval {
			t.Fatalf("expected default %v, got %v", defaultSiteTrafficSampleInterval, got)
		}
	})

	t.Run("allow off", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_SITE_TRAFFIC_SAMPLE_INTERVAL", "off")
		if got := resolveSiteTrafficSampleInterval(); got != 0 {
			t.Fatalf("expected disabled interval, got %v", got)
		}
	})

	t.Run("accept duration", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_SITE_TRAFFIC_SAMPLE_INTERVAL", "60s")
		if got := resolveSiteTrafficSampleInterval(); got != 60*time.Second {
			t.Fatalf("expected 60s, got %v", got)
		}
	})
}
