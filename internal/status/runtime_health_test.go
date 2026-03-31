package status

import (
	"testing"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/openvpn"
	"xiomi-router-driver/internal/subscription"
)

func TestProviderHealthUsesOpenVPNRuntimeState(t *testing.T) {
	provider := config.Provider{
		ID:      "provider-openvpn",
		Name:    "FizzVPN",
		Type:    config.ProviderTypeOpenVPN,
		Enabled: true,
		Source:  "profiles/nl.ovpn",
	}

	snapshot := &openvpn.RuntimeSnapshot{
		ProviderID:   provider.ID,
		Status:       "stopped",
		StatusDetail: "tunnel probe failed via tun0: timeout",
	}

	health, detail := providerHealth(provider, true, 1, snapshot, nil, nil)
	if health != "error" {
		t.Fatalf("providerHealth() health = %q, want error", health)
	}
	if detail != snapshot.StatusDetail {
		t.Fatalf("providerHealth() detail = %q, want %q", detail, snapshot.StatusDetail)
	}
}

func TestProviderHealthUsesSubscriptionRuntimeState(t *testing.T) {
	provider := config.Provider{
		ID:      "provider-sub",
		Name:    "FizzVPN Subscription",
		Type:    config.ProviderTypeSubscription,
		Enabled: true,
		Source:  "https://provider.example/sub",
	}

	snapshots := map[string]subscription.RuntimeSnapshot{
		"provider-sub::nl": {
			Key:          "provider-sub::nl",
			ProviderID:   provider.ID,
			Location:     "NL",
			Status:       "running",
			StatusDetail: "",
		},
		"provider-sub::us": {
			Key:          "provider-sub::us",
			ProviderID:   provider.ID,
			Location:     "US",
			Status:       "stopped",
			StatusDetail: "tunnel probe failed via sb123: timeout",
		},
	}

	health, detail := providerHealth(provider, true, 2, nil, []string{"provider-sub::nl", "provider-sub::us"}, snapshots)
	if health != "error" {
		t.Fatalf("providerHealth() health = %q, want error", health)
	}
	if detail != snapshots["provider-sub::us"].StatusDetail {
		t.Fatalf("providerHealth() detail = %q, want %q", detail, snapshots["provider-sub::us"].StatusDetail)
	}
}

func TestProviderHealthIgnoresSubscriptionRulesWithoutDomains(t *testing.T) {
	provider := config.Provider{
		ID:      "provider-sub",
		Name:    "FizzVPN Subscription",
		Type:    config.ProviderTypeSubscription,
		Enabled: true,
		Source:  "https://provider.example/sub",
	}
	state := config.State{
		Providers: []config.Provider{provider},
		Rules: []config.Rule{
			{
				ID:               "rule-empty",
				Name:             "Draft route",
				ProviderID:       provider.ID,
				SelectedLocation: "NL",
				Domains:          nil,
				Enabled:          true,
			},
		},
	}

	subscriptionKeys := expectedSubscriptionKeysByProvider(state)
	if len(subscriptionKeys[provider.ID]) != 0 {
		t.Fatalf("expected no runtime keys for empty-domain rule, got %+v", subscriptionKeys[provider.ID])
	}

	health, detail := providerHealth(provider, true, 1, nil, subscriptionKeys[provider.ID], map[string]subscription.RuntimeSnapshot{})
	if health != "warning" {
		t.Fatalf("providerHealth() health = %q, want warning", health)
	}
	if detail != "subscription provider has no active locations yet" {
		t.Fatalf("providerHealth() detail = %q, want subscription draft warning", detail)
	}
}
