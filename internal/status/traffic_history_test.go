package status

import (
	"testing"
	"time"
)

func TestAggregateTrafficHistoryBuildsDeltas(t *testing.T) {
	spec := trafficHistoryRangeSpec{
		Name:     "test",
		Lookback: 4 * time.Hour,
		Bucket:   time.Hour,
	}
	now := time.Date(2026, time.March, 25, 3, 30, 0, 0, time.UTC)

	history := aggregateTrafficHistory([]trafficHistorySample{
		{
			CollectedAt: "2026-03-25T00:00:00Z",
			Routes: []trafficHistoryRouteStat{
				testTrafficRoute("p1", "Provider A", "subscription", "US", "tun-us", 100, 50),
			},
		},
		{
			CollectedAt: "2026-03-25T01:00:00Z",
			Routes: []trafficHistoryRouteStat{
				testTrafficRoute("p1", "Provider A", "subscription", "US", "tun-us", 250, 100),
			},
		},
		{
			CollectedAt: "2026-03-25T02:00:00Z",
			Routes: []trafficHistoryRouteStat{
				testTrafficRoute("p1", "Provider A", "subscription", "US", "tun-us", 300, 150),
				testTrafficRoute("p2", "Provider B", "subscription", "NL", "tun-nl", 20, 10),
			},
		},
		{
			CollectedAt: "2026-03-25T03:00:00Z",
			Routes: []trafficHistoryRouteStat{
				testTrafficRoute("p1", "Provider A", "subscription", "US", "tun-us", 20, 10),
				testTrafficRoute("p2", "Provider B", "subscription", "NL", "tun-nl", 50, 40),
			},
		},
	}, spec, now, 10*time.Minute)

	if history.TotalBytes != 390 {
		t.Fatalf("expected total bytes 390, got %d", history.TotalBytes)
	}
	if len(history.Breakdown) != 2 {
		t.Fatalf("expected 2 breakdown items, got %d", len(history.Breakdown))
	}
	if len(history.RouteSeries) != 2 {
		t.Fatalf("expected 2 route series items, got %d", len(history.RouteSeries))
	}
	if history.Breakdown[0].ProviderID != "p1" || history.Breakdown[0].TotalBytes != 330 {
		t.Fatalf("unexpected first breakdown: %+v", history.Breakdown[0])
	}
	if history.Breakdown[1].ProviderID != "p2" || history.Breakdown[1].TotalBytes != 60 {
		t.Fatalf("unexpected second breakdown: %+v", history.Breakdown[1])
	}
	if history.RouteSeries[0].ProviderID != "p1" || history.RouteSeries[0].TotalBytes != 330 || history.RouteSeries[0].PeakBytes != 200 {
		t.Fatalf("unexpected first route series: %+v", history.RouteSeries[0])
	}
	if history.RouteSeries[1].ProviderID != "p2" || history.RouteSeries[1].TotalBytes != 60 || history.RouteSeries[1].PeakBytes != 60 {
		t.Fatalf("unexpected second route series: %+v", history.RouteSeries[1])
	}
	if len(history.RouteSeries[0].Points) != len(history.Points) {
		t.Fatalf("expected route series points to match chart points, got %d vs %d", len(history.RouteSeries[0].Points), len(history.Points))
	}

	var summed uint64
	for _, point := range history.Points {
		summed += point.TotalBytes
	}
	if summed != history.TotalBytes {
		t.Fatalf("points total %d does not match history total %d", summed, history.TotalBytes)
	}
}

func TestCounterDeltaHandlesCounterReset(t *testing.T) {
	if got := counterDelta(20, 300); got != 20 {
		t.Fatalf("expected counter delta 20 after reset, got %d", got)
	}
	if got := counterDelta(320, 300); got != 20 {
		t.Fatalf("expected regular counter delta 20, got %d", got)
	}
}

func testTrafficRoute(providerID string, providerName string, providerType string, location string, iface string, rx uint64, tx uint64) trafficHistoryRouteStat {
	return trafficHistoryRouteStat{
		ProviderID:    providerID,
		ProviderName:  providerName,
		ProviderType:  providerType,
		Location:      location,
		InterfaceName: iface,
		RXBytes:       rx,
		TXBytes:       tx,
	}
}
