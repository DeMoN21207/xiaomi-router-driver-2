package status

import (
	"testing"
	"time"
)

func TestDomainTrafficStoreListAppliesSortAndLimitInQuery(t *testing.T) {
	store := newDomainTrafficStore(openSiteTrafficTestDB(t))
	now := time.Date(2026, time.March, 29, 10, 0, 0, 0, time.UTC).Format(time.RFC3339)

	if err := store.Upsert([]DomainTrafficStat{
		{Domain: "alpha.com", Bytes: 120, Packets: 3},
		{Domain: "beta.com", Bytes: 420, Packets: 8},
		{Domain: "gamma.com", Bytes: 240, Packets: 5},
	}, now); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	byBytes, err := store.List("bytes", 2)
	if err != nil {
		t.Fatalf("List(bytes, 2) error = %v", err)
	}
	if byBytes.TotalBytes != 780 {
		t.Fatalf("expected total bytes 780, got %d", byBytes.TotalBytes)
	}
	if byBytes.UpdatedAt != now {
		t.Fatalf("expected updatedAt %q, got %q", now, byBytes.UpdatedAt)
	}
	if len(byBytes.Stats) != 2 {
		t.Fatalf("expected 2 stats, got %d", len(byBytes.Stats))
	}
	if byBytes.Stats[0].Domain != "beta.com" || byBytes.Stats[1].Domain != "gamma.com" {
		t.Fatalf("unexpected byte order: %+v", byBytes.Stats)
	}

	byDomain, err := store.List("domain", 0)
	if err != nil {
		t.Fatalf("List(domain, 0) error = %v", err)
	}
	if len(byDomain.Stats) != 3 {
		t.Fatalf("expected 3 stats, got %d", len(byDomain.Stats))
	}
	if byDomain.Stats[0].Domain != "alpha.com" || byDomain.Stats[1].Domain != "beta.com" || byDomain.Stats[2].Domain != "gamma.com" {
		t.Fatalf("unexpected domain order: %+v", byDomain.Stats)
	}
}
