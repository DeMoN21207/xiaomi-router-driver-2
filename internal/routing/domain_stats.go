package routing

import (
	"os"
	"strconv"
	"strings"
)

const defaultDomainStatsMaxDomains = 256

func DomainStatsMaxDomains() int {
	return parseDomainStatsMaxDomains(
		strings.TrimSpace(os.Getenv("VPN_MANAGER_DOMAIN_STATS_MAX_DOMAINS")),
		strings.TrimSpace(os.Getenv("DOMAIN_STATS_MAX_DOMAINS")),
	)
}

func DomainStatsEnabled(domainCount int) bool {
	maxDomains := DomainStatsMaxDomains()
	if maxDomains <= 0 {
		return true
	}
	return domainCount <= maxDomains
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
