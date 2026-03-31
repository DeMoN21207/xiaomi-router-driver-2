package openvpn

import (
	"path/filepath"
	"testing"

	"xiomi-router-driver/internal/config"
)

func TestSameRuntimeConfig(t *testing.T) {
	settings := config.DefaultRoutingSettings()
	settings.VPNIface = "tun9"

	instance := &managedInstance{
		ProviderID:    "provider-1",
		ProviderName:  "FizzVPN",
		InterfaceName: "tun9",
		ProfilePath:   filepath.Join("profiles", "de.ovpn"),
		Settings:      settings,
	}

	if !sameRuntimeConfig(instance, filepath.Join("profiles", ".", "de.ovpn"), settings) {
		t.Fatalf("expected runtime config to match")
	}

	otherSettings := settings
	otherSettings.TableNum++
	if sameRuntimeConfig(instance, filepath.Join("profiles", "de.ovpn"), otherSettings) {
		t.Fatalf("expected runtime config mismatch when settings change")
	}
}
