package status

import "testing"

func TestParseSystemUptime(t *testing.T) {
	uptime := parseSystemUptime("90061.23 120.00\n")

	if uptime.Seconds != 90061 {
		t.Fatalf("Seconds = %d, want 90061", uptime.Seconds)
	}
	if uptime.Formatted != "1д 1ч 1м" {
		t.Fatalf("Formatted = %q, want 1д 1ч 1м", uptime.Formatted)
	}
}

func TestApplyMemInfoReportsAvailableCacheAndKernelBreakdown(t *testing.T) {
	var res SystemResources

	applyMemInfo(&res, `MemTotal:         804516 kB
MemFree:           91316 kB
MemAvailable:     346372 kB
Buffers:           11616 kB
Cached:           299376 kB
KReclaimable:      20480 kB
Slab:             291004 kB
SReclaimable:      20480 kB
SUnreclaim:       270524 kB
SwapTotal:        131068 kB
SwapFree:         119204 kB
`)

	if res.MemTotalMB != 785 {
		t.Fatalf("MemTotalMB = %d, want 785", res.MemTotalMB)
	}
	if res.MemUsedMB != 392 {
		t.Fatalf("MemUsedMB = %d, want 392", res.MemUsedMB)
	}
	if res.MemFreeMB != 89 {
		t.Fatalf("MemFreeMB = %d, want 89", res.MemFreeMB)
	}
	if res.MemAvailableMB != 338 {
		t.Fatalf("MemAvailableMB = %d, want 338", res.MemAvailableMB)
	}
	if res.MemCacheMB != 323 {
		t.Fatalf("MemCacheMB = %d, want 323", res.MemCacheMB)
	}
	if res.MemSlabMB != 284 {
		t.Fatalf("MemSlabMB = %d, want 284", res.MemSlabMB)
	}
	if res.MemKernelUnreclaimableMB != 264 {
		t.Fatalf("MemKernelUnreclaimableMB = %d, want 264", res.MemKernelUnreclaimableMB)
	}
	if res.SwapUsedMB != 11 {
		t.Fatalf("SwapUsedMB = %d, want 11", res.SwapUsedMB)
	}
}
