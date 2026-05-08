package config

import (
	"strings"
	"time"
)

const (
	RoutingLoadProfileMinimal  = "minimal"
	RoutingLoadProfileNormal   = "normal"
	RoutingLoadProfileDetailed = "detailed"
)

type RoutingLoadProfileTuning struct {
	DomainStatsMode             string
	DomainStatsMaxDomains       int
	PrimeMaxDomains             int
	IPSetFlushOnSync            bool
	IPSetTimeout                int
	ConntrackFlushOnApply       bool
	DomainTrafficSampleInterval time.Duration
	SiteTrafficSampleInterval   time.Duration
	DomainHealthInitialSample   bool
	DomainHealthSampleInterval  time.Duration
}

func DefaultRoutingLoadProfile() string {
	return RoutingLoadProfileNormal
}

func NormalizeRoutingLoadProfile(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case RoutingLoadProfileMinimal:
		return RoutingLoadProfileMinimal
	case "", RoutingLoadProfileNormal:
		return RoutingLoadProfileNormal
	case RoutingLoadProfileDetailed:
		return RoutingLoadProfileDetailed
	default:
		return RoutingLoadProfileNormal
	}
}

func RoutingLoadProfileTuningFor(profile string) RoutingLoadProfileTuning {
	switch NormalizeRoutingLoadProfile(profile) {
	case RoutingLoadProfileMinimal:
		return RoutingLoadProfileTuning{
			DomainStatsMode:             "off",
			DomainStatsMaxDomains:       128,
			PrimeMaxDomains:             0,
			IPSetFlushOnSync:            false,
			IPSetTimeout:                1800,
			ConntrackFlushOnApply:       false,
			DomainTrafficSampleInterval: 0,
			SiteTrafficSampleInterval:   0,
			DomainHealthInitialSample:   false,
			DomainHealthSampleInterval:  24 * time.Hour,
		}
	case RoutingLoadProfileDetailed:
		return RoutingLoadProfileTuning{
			DomainStatsMode:             "on",
			DomainStatsMaxDomains:       128,
			PrimeMaxDomains:             512,
			IPSetFlushOnSync:            false,
			IPSetTimeout:                86400,
			ConntrackFlushOnApply:       true,
			DomainTrafficSampleInterval: 30 * time.Second,
			SiteTrafficSampleInterval:   30 * time.Second,
			DomainHealthInitialSample:   true,
			DomainHealthSampleInterval:  12 * time.Hour,
		}
	default:
		return RoutingLoadProfileTuning{
			DomainStatsMode:             "auto",
			DomainStatsMaxDomains:       128,
			PrimeMaxDomains:             512,
			IPSetFlushOnSync:            false,
			IPSetTimeout:                86400,
			ConntrackFlushOnApply:       true,
			DomainTrafficSampleInterval: 120 * time.Second,
			SiteTrafficSampleInterval:   0,
			DomainHealthInitialSample:   false,
			DomainHealthSampleInterval:  24 * time.Hour,
		}
	}
}
