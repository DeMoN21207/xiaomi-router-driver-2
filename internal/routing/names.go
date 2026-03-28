package routing

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
)

const (
	domainStatsChainPrefix = "VDS_"
	maxIptablesChainLen    = 28 // iptables requires names to be under 29 chars
)

func DomainStatsChainName(ipsetName string) string {
	ipsetName = strings.TrimSpace(ipsetName)
	if ipsetName == "" {
		return domainStatsChainPrefix + "default"
	}

	name := domainStatsChainPrefix + ipsetName
	if len(name) <= maxIptablesChainLen {
		return name
	}

	hash := sha1.Sum([]byte(ipsetName))
	suffix := hex.EncodeToString(hash[:])[:8]
	available := maxIptablesChainLen - len(domainStatsChainPrefix) - 1 - len(suffix)
	if available < 1 {
		available = 1
	}
	if available > len(ipsetName) {
		available = len(ipsetName)
	}

	return domainStatsChainPrefix + ipsetName[:available] + "_" + suffix
}

func LegacyDomainStatsChainName(ipsetName string) string {
	return "VPN_DOM_STATS_" + strings.TrimSpace(ipsetName)
}
