package status

import (
	"testing"
	"time"
)

func TestRecommendDomainDecision(t *testing.T) {
	t.Run("keep when vpn path is healthy", func(t *testing.T) {
		decision := recommendDomainDecision(DomainHealthRecord{
			VPNDNSStatus:       domainDNSStatusOK,
			VPNTransportStatus: domainTransportStatusOK,
		})
		if decision != domainDecisionKeep {
			t.Fatalf("expected keep, got %q", decision)
		}
	})

	t.Run("review when only direct path is healthy", func(t *testing.T) {
		decision := recommendDomainDecision(DomainHealthRecord{
			DirectDNSStatus:       domainDNSStatusOK,
			DirectTransportStatus: domainTransportStatusOK,
			VPNDNSStatus:          domainDNSStatusRuntimeDown,
			VPNTransportStatus:    domainTransportStatusRuntimeDown,
		})
		if decision != domainDecisionReview {
			t.Fatalf("expected review, got %q", decision)
		}
	})

	t.Run("candidate after repeated dns failures on both paths", func(t *testing.T) {
		decision := recommendDomainDecision(DomainHealthRecord{
			DirectDNSStatus:              domainDNSStatusNXDOMAIN,
			VPNDNSStatus:                 domainDNSStatusNXDOMAIN,
			ConsecutiveDirectDNSFailures: domainHealthDNSCandidateFailures,
			ConsecutiveVPNDNSFailures:    domainHealthDNSCandidateFailures,
			DirectTransportStatus:        domainTransportStatusUnknown,
			VPNTransportStatus:           domainTransportStatusUnknown,
		})
		if decision != domainDecisionDeleteCandidate {
			t.Fatalf("expected delete candidate, got %q", decision)
		}
	})
}

func TestResolveDomainHealthSampleInterval(t *testing.T) {
	t.Run("default interval", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DOMAIN_HEALTH_INTERVAL", "")
		if got := resolveDomainHealthSampleInterval(); got != defaultDomainHealthSampleInterval {
			t.Fatalf("expected default interval %v, got %v", defaultDomainHealthSampleInterval, got)
		}
	})

	t.Run("accept duration", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DOMAIN_HEALTH_INTERVAL", "6h")
		if got := resolveDomainHealthSampleInterval(); got != 6*time.Hour {
			t.Fatalf("expected 6h, got %v", got)
		}
	})

	t.Run("accept hour shorthand", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DOMAIN_HEALTH_INTERVAL", "24")
		if got := resolveDomainHealthSampleInterval(); got != 24*time.Hour {
			t.Fatalf("expected 24h, got %v", got)
		}
	})

	t.Run("allow off", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DOMAIN_HEALTH_INTERVAL", "off")
		if got := resolveDomainHealthSampleInterval(); got != 0 {
			t.Fatalf("expected disabled interval, got %v", got)
		}
	})
}

func TestDomainHealthInitialSampleEnabled(t *testing.T) {
	t.Run("enabled by default", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DOMAIN_HEALTH_INITIAL_SAMPLE", "")
		if !domainHealthInitialSampleEnabled() {
			t.Fatalf("expected initial sample enabled by default")
		}
	})

	t.Run("explicit off disables initial sample", func(t *testing.T) {
		t.Setenv("VPN_MANAGER_DOMAIN_HEALTH_INITIAL_SAMPLE", "off")
		if domainHealthInitialSampleEnabled() {
			t.Fatalf("expected initial sample disabled")
		}
	})
}
