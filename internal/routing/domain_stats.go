package routing

import (
	"os"
	"strconv"
	"strings"
)

const defaultDomainStatsMaxDomains = 128
const hardDomainStatsLimit = 128

func DomainStatsMaxDomains() int {
	return DomainStatsMaxDomainsWithFallback(defaultDomainStatsMaxDomains)
}

func DomainStatsMaxDomainsWithFallback(fallback int) int {
	return parseDomainStatsMaxDomains(
		strings.TrimSpace(os.Getenv("VPN_MANAGER_DOMAIN_STATS_MAX_DOMAINS")),
		strings.TrimSpace(os.Getenv("DOMAIN_STATS_MAX_DOMAINS")),
		strconv.Itoa(fallback),
	)
}

func DomainStatsEnabled(domainCount int) bool {
	return DomainStatsEnabledWithOptions(domainCount, DomainStatsModeWithFallback("auto"), DomainStatsMaxDomains())
}

func DomainStatsEnabledWithOptions(domainCount int, mode string, maxDomains int) bool {
	if domainCount > hardDomainStatsLimit && !allowHeavyDomainStats() {
		return false
	}

	switch NormalizeDomainStatsMode(mode) {
	case "off":
		return false
	case "on":
		return true
	}

	if maxDomains <= 0 {
		return true
	}
	return domainCount <= maxDomains
}

func allowHeavyDomainStats() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("VPN_MANAGER_ALLOW_HEAVY_DOMAIN_STATS"))) {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}

func DomainStatsModeWithFallback(fallback string) string {
	for _, value := range []string{
		strings.TrimSpace(os.Getenv("VPN_MANAGER_DOMAIN_STATS")),
		strings.TrimSpace(os.Getenv("DOMAIN_STATS")),
	} {
		if value != "" {
			return NormalizeDomainStatsMode(value)
		}
	}
	mode := NormalizeDomainStatsMode(fallback)
	if mode == "" {
		return "auto"
	}
	return mode
}

func NormalizeDomainStatsMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "off", "false", "disabled":
		return "off"
	case "1", "on", "true", "enabled":
		return "on"
	case "", "auto":
		return "auto"
	default:
		return ""
	}
}

func parseDomainStatsMaxDomains(values ...string) int {
	for _, value := range values {
		if value == "" {
			continue
		}

		parsed, err := strconv.Atoi(value)
		if err != nil {
			continue
		}
		return parsed
	}

	return defaultDomainStatsMaxDomains
}
