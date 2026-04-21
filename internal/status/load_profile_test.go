package status

import (
	"path/filepath"
	"testing"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/sqlitedb"
)

func TestServiceEffectiveIntervalsFollowRoutingLoadProfile(t *testing.T) {
	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "vpn-manager.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	stateManager := config.NewManager(db, filepath.Join(t.TempDir(), "vpn-state.json"))
	service := &Service{
		state:                       stateManager,
		domainTrafficSampleInterval: defaultDomainTrafficSampleInterval,
		domainHealthSampleInterval:  defaultDomainHealthSampleInterval,
		siteTrafficSampleInterval:   defaultSiteTrafficSampleInterval,
	}

	state := config.DefaultState()
	state.Routing.LoadProfile = config.RoutingLoadProfileMinimal
	if _, err := stateManager.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if got := service.effectiveDomainTrafficSampleInterval(); got != 0 {
		t.Fatalf("expected minimal domain traffic interval 0, got %v", got)
	}
	if got := service.effectiveSiteTrafficSampleInterval(); got != 0 {
		t.Fatalf("expected minimal site traffic interval 0, got %v", got)
	}
	if got := service.effectiveDomainHealthSampleInterval(); got != 24*time.Hour {
		t.Fatalf("expected minimal domain health interval 24h, got %v", got)
	}
	if service.effectiveDomainHealthInitialSampleEnabled() {
		t.Fatalf("expected minimal profile to disable initial domain health sample")
	}
	if service.effectiveDomainStatsEnabled(1) {
		t.Fatalf("expected minimal profile to disable domain stats")
	}

	state.Routing.LoadProfile = config.RoutingLoadProfileDetailed
	if _, err := stateManager.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if got := service.effectiveDomainTrafficSampleInterval(); got != 30*time.Second {
		t.Fatalf("expected detailed domain traffic interval 30s, got %v", got)
	}
	if got := service.effectiveSiteTrafficSampleInterval(); got != 30*time.Second {
		t.Fatalf("expected detailed site traffic interval 30s, got %v", got)
	}
	if got := service.effectiveDomainHealthSampleInterval(); got != 12*time.Hour {
		t.Fatalf("expected detailed domain health interval 12h, got %v", got)
	}
	if !service.effectiveDomainHealthInitialSampleEnabled() {
		t.Fatalf("expected detailed profile to enable initial domain health sample")
	}
	if service.effectiveDomainStatsEnabled(200) {
		t.Fatalf("expected hard cap to disable domain stats above 128 domains")
	}

	state.Routing.LoadProfile = config.RoutingLoadProfileNormal
	if _, err := stateManager.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if got := service.effectiveSiteTrafficSampleInterval(); got != 0 {
		t.Fatalf("expected normal profile to keep site observer disabled, got %v", got)
	}
}
