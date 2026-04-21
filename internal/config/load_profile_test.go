package config

import (
	"testing"
	"time"
)

func TestNormalizeRoutingLoadProfile(t *testing.T) {
	if got := NormalizeRoutingLoadProfile(""); got != RoutingLoadProfileNormal {
		t.Fatalf("expected default profile %q, got %q", RoutingLoadProfileNormal, got)
	}
	if got := NormalizeRoutingLoadProfile("MINIMAL"); got != RoutingLoadProfileMinimal {
		t.Fatalf("expected minimal, got %q", got)
	}
	if got := NormalizeRoutingLoadProfile("unknown"); got != RoutingLoadProfileNormal {
		t.Fatalf("expected fallback profile %q, got %q", RoutingLoadProfileNormal, got)
	}
}

func TestRoutingLoadProfileTuningFor(t *testing.T) {
	minimal := RoutingLoadProfileTuningFor(RoutingLoadProfileMinimal)
	if minimal.DomainStatsMode != "off" || minimal.DomainTrafficSampleInterval != 0 || minimal.SiteTrafficSampleInterval != 0 {
		t.Fatalf("unexpected minimal tuning: %+v", minimal)
	}

	normal := RoutingLoadProfileTuningFor(RoutingLoadProfileNormal)
	if normal.DomainStatsMode != "auto" || normal.DomainTrafficSampleInterval != 120*time.Second || normal.SiteTrafficSampleInterval != 0 {
		t.Fatalf("unexpected normal tuning: %+v", normal)
	}

	detailed := RoutingLoadProfileTuningFor(RoutingLoadProfileDetailed)
	if detailed.DomainStatsMode != "on" || detailed.DomainHealthSampleInterval != 12*time.Hour || !detailed.DomainHealthInitialSample || detailed.SiteTrafficSampleInterval != 30*time.Second {
		t.Fatalf("unexpected detailed tuning: %+v", detailed)
	}
}
