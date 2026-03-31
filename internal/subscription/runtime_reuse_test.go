package subscription

import (
	"os"
	"path/filepath"
	"testing"

	"xiomi-router-driver/internal/config"
)

func TestSameRuntimeConfig(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "nl.json")
	configData := []byte("{\"outbounds\":[{\"tag\":\"proxy\"}]}\n")
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	settings := config.DefaultRoutingSettings()
	settings.VPNIface = "sbnl123456"

	plan := applyPlan{
		desired: desiredInstance{
			Key: "provider_1::nl",
			Provider: config.Provider{
				ID:   "provider_1",
				Name: "FizzVPN",
			},
			Location: "NL",
		},
		settings:   settings,
		configPath: configPath,
		configData: configData,
	}

	instance := &managedInstance{
		Key:           "provider_1::nl",
		ProviderID:    "provider_1",
		ProviderName:  "FizzVPN",
		Location:      "NL",
		InterfaceName: "sbnl123456",
		ConfigPath:    configPath,
		Settings:      settings,
	}

	if !sameRuntimeConfig(instance, plan) {
		t.Fatalf("expected runtime config to match")
	}

	otherPlan := plan
	otherPlan.settings = settings
	otherPlan.settings.TableNum++
	if sameRuntimeConfig(instance, otherPlan) {
		t.Fatalf("expected runtime config mismatch when settings change")
	}
}

func TestShouldRecordRuntimeLogSkipsUnsupportedICMPWarning(t *testing.T) {
	line := `WARN inbound/tun[tun-in]: link icmp connection from 172.29.0.1 to 1.1.1.1: icmp is not supported by default outbound: proxy`
	if shouldRecordRuntimeLog(line, "warn") {
		t.Fatalf("expected noisy sing-box icmp warning to be skipped")
	}
}

func TestShouldRecordRuntimeLogSkipsInfoNoise(t *testing.T) {
	line := `INFO inbound/tun[tun-in]: inbound packet connection from 172.29.0.1:49260`
	if shouldRecordRuntimeLog(line, "info") {
		t.Fatalf("expected runtime info noise to be skipped")
	}
}
