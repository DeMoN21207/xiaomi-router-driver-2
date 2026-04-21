package status

import (
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/routing"
)

const disabledSamplerPollInterval = 15 * time.Second

func (s *Service) routingLoadProfileTuning() (config.RoutingLoadProfileTuning, bool) {
	defaultTuning := config.RoutingLoadProfileTuningFor(config.DefaultRoutingLoadProfile())
	if s == nil || s.state == nil {
		return defaultTuning, false
	}

	state, err := s.state.Load()
	if err != nil {
		return defaultTuning, false
	}

	return config.RoutingLoadProfileTuningFor(state.Routing.LoadProfile), true
}

func (s *Service) effectiveDomainStatsEnabled(domainCount int) bool {
	tuning, ok := s.routingLoadProfileTuning()
	if !ok {
		return routing.DomainStatsEnabled(domainCount)
	}

	return routing.DomainStatsEnabledWithOptions(
		domainCount,
		routing.DomainStatsModeWithFallback(tuning.DomainStatsMode),
		routing.DomainStatsMaxDomainsWithFallback(tuning.DomainStatsMaxDomains),
	)
}

func (s *Service) effectiveDomainTrafficSampleInterval() time.Duration {
	tuning, ok := s.routingLoadProfileTuning()
	fallback := s.domainTrafficSampleInterval
	if ok {
		fallback = tuning.DomainTrafficSampleInterval
	}

	return resolveOptionalDurationEnv(
		"VPN_MANAGER_DOMAIN_TRAFFIC_SAMPLE_INTERVAL",
		fallback,
		5*time.Second,
	)
}

func (s *Service) effectiveSiteTrafficSampleInterval() time.Duration {
	tuning, ok := s.routingLoadProfileTuning()
	fallback := s.siteTrafficSampleInterval
	if ok {
		fallback = tuning.SiteTrafficSampleInterval
	}

	return resolveOptionalDurationEnv(
		"VPN_MANAGER_SITE_TRAFFIC_SAMPLE_INTERVAL",
		fallback,
		5*time.Second,
	)
}

func (s *Service) effectiveDomainHealthSampleInterval() time.Duration {
	tuning, ok := s.routingLoadProfileTuning()
	fallback := s.domainHealthSampleInterval
	if ok {
		fallback = tuning.DomainHealthSampleInterval
	}

	return resolveDomainHealthSampleIntervalWithFallback(fallback)
}

func (s *Service) effectiveDomainHealthInitialSampleEnabled() bool {
	tuning, ok := s.routingLoadProfileTuning()
	fallback := true
	if ok {
		fallback = tuning.DomainHealthInitialSample
	}

	return resolveDomainHealthInitialSampleEnabledWithFallback(fallback)
}
