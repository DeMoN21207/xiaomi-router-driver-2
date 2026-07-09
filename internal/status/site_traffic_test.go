package status

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/sqlitedb"
)

func TestSiteTrafficStoreListSupportsSourceIPFilterAndPagination(t *testing.T) {
	store := newSiteTrafficStore(openSiteTrafficTestDB(t))
	now := time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC).Format(time.RFC3339)

	if err := store.UpsertConnections([]siteTrafficConnection{
		{
			Key:        "tcp|192.168.31.10|104.18.33.45|50000|443",
			SourceIP:   "192.168.31.10",
			DeviceName: "Galaxy-S25-Ultra",
			DeviceMAC:  "aa:bb:cc:dd:ee:01",
			Domain:     "openai.com",
			LastIP:     "104.18.33.45",
			Bytes:      4096,
			Packets:    32,
			ViaTunnel:  true,
			RouteLabel: "FizzVPN / NL",
		},
		{
			Key:        "tcp|192.168.31.10|151.101.1.164|50001|443",
			SourceIP:   "192.168.31.10",
			DeviceName: "Galaxy-S25-Ultra",
			DeviceMAC:  "aa:bb:cc:dd:ee:01",
			Domain:     "chatgpt.com",
			LastIP:     "151.101.1.164",
			Bytes:      2048,
			Packets:    18,
			ViaTunnel:  true,
			RouteLabel: "FizzVPN / NL",
		},
		{
			Key:        "tcp|192.168.31.20|142.250.185.206|51000|443",
			SourceIP:   "192.168.31.20",
			DeviceName: "LGwebOSTV",
			DeviceMAC:  "aa:bb:cc:dd:ee:02",
			Domain:     "youtube.com",
			LastIP:     "142.250.185.206",
			Bytes:      8192,
			Packets:    64,
			ViaTunnel:  false,
			RouteLabel: "",
		},
	}, now); err != nil {
		t.Fatalf("UpsertConnections() error = %v", err)
	}

	result, err := store.List("all", "bytes", "", "", "", 1, 2)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if result.TotalCount != 3 {
		t.Fatalf("expected total count 3, got %d", result.TotalCount)
	}
	if len(result.Stats) != 2 {
		t.Fatalf("expected 2 paged stats, got %d", len(result.Stats))
	}
	if result.Stats[0].Domain != "youtube.com" {
		t.Fatalf("expected first paged stat to be youtube.com, got %q", result.Stats[0].Domain)
	}
	if result.TotalBytes != 14336 {
		t.Fatalf("expected total bytes 14336, got %d", result.TotalBytes)
	}

	filtered, err := store.List("tunneled", "domain", "asc", "192.168.31.10", "", 1, 10)
	if err != nil {
		t.Fatalf("List() with source filter error = %v", err)
	}
	if filtered.TotalCount != 2 {
		t.Fatalf("expected filtered total count 2, got %d", filtered.TotalCount)
	}
	if len(filtered.Stats) != 2 {
		t.Fatalf("expected 2 filtered stats, got %d", len(filtered.Stats))
	}
	if filtered.Stats[0].Domain != "chatgpt.com" || filtered.Stats[1].Domain != "openai.com" {
		t.Fatalf("unexpected filtered domains: %#v", filtered.Stats)
	}
	if filtered.TotalBytes != 6144 {
		t.Fatalf("expected filtered bytes 6144, got %d", filtered.TotalBytes)
	}

	direct, err := store.List("direct", "packets", "asc", "", "", 1, 10)
	if err != nil {
		t.Fatalf("List() direct scope error = %v", err)
	}
	if direct.TotalCount != 1 {
		t.Fatalf("expected one direct site, got %d", direct.TotalCount)
	}
	if len(direct.Stats) != 1 || direct.Stats[0].Domain != "youtube.com" || direct.Stats[0].ViaTunnel {
		t.Fatalf("unexpected direct scope stats: %+v", direct.Stats)
	}
}

func TestSiteTrafficStoreListCoversSortScopeSearchSourceAndPages(t *testing.T) {
	store := newSiteTrafficStore(openSiteTrafficTestDB(t))
	seedTrafficMatrix(t, store)

	tests := []struct {
		name    string
		scope   string
		sortBy  string
		order   string
		source  string
		search  string
		page    int
		size    int
		total   int
		bytes   uint64
		domains []string
	}{
		{
			name:    "bytes desc default",
			sortBy:  "bytes",
			page:    1,
			size:    10,
			total:   4,
			bytes:   10000,
			domains: []string{"delta.org", "alpha.com", "gamma.net", "beta.com"},
		},
		{
			name:    "bytes asc",
			sortBy:  "bytes",
			order:   "asc",
			page:    1,
			size:    10,
			total:   4,
			bytes:   10000,
			domains: []string{"beta.com", "gamma.net", "alpha.com", "delta.org"},
		},
		{
			name:    "packets asc",
			sortBy:  "packets",
			order:   "asc",
			page:    1,
			size:    10,
			total:   4,
			bytes:   10000,
			domains: []string{"gamma.net", "alpha.com", "delta.org", "beta.com"},
		},
		{
			name:    "domain desc",
			sortBy:  "domain",
			order:   "desc",
			page:    1,
			size:    10,
			total:   4,
			bytes:   10000,
			domains: []string{"gamma.net", "delta.org", "beta.com", "alpha.com"},
		},
		{
			name:    "updated asc",
			sortBy:  "updated",
			order:   "asc",
			page:    1,
			size:    10,
			total:   4,
			bytes:   10000,
			domains: []string{"alpha.com", "beta.com", "gamma.net", "delta.org"},
		},
		{
			name:    "tunneled scope",
			scope:   "tunneled",
			sortBy:  "domain",
			order:   "asc",
			page:    1,
			size:    10,
			total:   2,
			bytes:   5000,
			domains: []string{"alpha.com", "gamma.net"},
		},
		{
			name:    "direct scope",
			scope:   "direct",
			sortBy:  "bytes",
			page:    1,
			size:    10,
			total:   2,
			bytes:   5000,
			domains: []string{"delta.org", "beta.com"},
		},
		{
			name:    "source ip filter",
			source:  "192.168.31.20",
			sortBy:  "domain",
			order:   "asc",
			page:    1,
			size:    10,
			total:   2,
			bytes:   5000,
			domains: []string{"beta.com", "delta.org"},
		},
		{
			name:    "domain search",
			search:  "GAMMA",
			sortBy:  "bytes",
			page:    1,
			size:    10,
			total:   1,
			bytes:   2000,
			domains: []string{"gamma.net"},
		},
		{
			name:    "last ip search",
			search:  "198.51.100.20",
			sortBy:  "bytes",
			page:    1,
			size:    10,
			total:   1,
			bytes:   1000,
			domains: []string{"beta.com"},
		},
		{
			name:    "page two",
			sortBy:  "bytes",
			page:    2,
			size:    2,
			total:   4,
			bytes:   10000,
			domains: []string{"gamma.net", "beta.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := store.List(tt.scope, tt.sortBy, tt.order, tt.source, tt.search, tt.page, tt.size)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if result.TotalCount != tt.total {
				t.Fatalf("TotalCount = %d, want %d", result.TotalCount, tt.total)
			}
			if result.TotalBytes != tt.bytes {
				t.Fatalf("TotalBytes = %d, want %d", result.TotalBytes, tt.bytes)
			}
			gotDomains := make([]string, 0, len(result.Stats))
			for _, item := range result.Stats {
				gotDomains = append(gotDomains, item.Domain)
			}
			if strings.Join(gotDomains, ",") != strings.Join(tt.domains, ",") {
				t.Fatalf("domains = %#v, want %#v", gotDomains, tt.domains)
			}
		})
	}
}

func TestSiteTrafficStoreListDevicesSupportsSearchPaginationAndOptions(t *testing.T) {
	store := newSiteTrafficStore(openSiteTrafficTestDB(t))
	now := time.Date(2026, time.March, 26, 12, 15, 0, 0, time.UTC).Format(time.RFC3339)

	if err := store.UpsertConnections([]siteTrafficConnection{
		{
			Key:        "a",
			SourceIP:   "192.168.31.10",
			DeviceName: "Galaxy-S25-Ultra",
			DeviceMAC:  "aa:bb:cc:dd:ee:01",
			Domain:     "openai.com",
			LastIP:     "104.18.33.45",
			Bytes:      4096,
			Packets:    32,
			ViaTunnel:  true,
			RouteLabel: "FizzVPN / NL",
		},
		{
			Key:        "b",
			SourceIP:   "192.168.31.10",
			DeviceName: "Galaxy-S25-Ultra",
			DeviceMAC:  "aa:bb:cc:dd:ee:01",
			Domain:     "chatgpt.com",
			LastIP:     "151.101.1.164",
			Bytes:      2048,
			Packets:    18,
			ViaTunnel:  true,
			RouteLabel: "FizzVPN / NL",
		},
		{
			Key:        "c",
			SourceIP:   "192.168.31.20",
			DeviceName: "LGwebOSTV",
			DeviceMAC:  "aa:bb:cc:dd:ee:02",
			Domain:     "youtube.com",
			LastIP:     "142.250.185.206",
			Bytes:      8192,
			Packets:    64,
			ViaTunnel:  false,
			RouteLabel: "",
		},
	}, now); err != nil {
		t.Fatalf("UpsertConnections() error = %v", err)
	}

	result, err := store.ListDevices("all", "bytes", "", "", "", 1, 1, 1)
	if err != nil {
		t.Fatalf("ListDevices() error = %v", err)
	}
	if result.TotalCount != 2 {
		t.Fatalf("expected total device count 2, got %d", result.TotalCount)
	}
	if len(result.Devices) != 1 {
		t.Fatalf("expected one paged device, got %d", len(result.Devices))
	}
	if result.Devices[0].SourceIP != "192.168.31.20" {
		t.Fatalf("expected first device to be 192.168.31.20, got %q", result.Devices[0].SourceIP)
	}
	if len(result.Devices[0].Sites) != 1 {
		t.Fatalf("expected top sites limit 1, got %d", len(result.Devices[0].Sites))
	}
	if len(result.Options) != 2 {
		t.Fatalf("expected 2 device options, got %d", len(result.Options))
	}

	filtered, err := store.ListDevices("tunneled", "name", "", "", "chatgpt", 1, 10, 10)
	if err != nil {
		t.Fatalf("ListDevices() with search error = %v", err)
	}
	if filtered.TotalCount != 1 {
		t.Fatalf("expected searched device count 1, got %d", filtered.TotalCount)
	}
	if len(filtered.Devices) != 1 || filtered.Devices[0].SourceIP != "192.168.31.10" {
		t.Fatalf("unexpected filtered devices: %#v", filtered.Devices)
	}
	if filtered.Devices[0].TunneledBytes != 6144 {
		t.Fatalf("expected tunneled bytes 6144, got %d", filtered.Devices[0].TunneledBytes)
	}

	sourceFiltered, err := store.ListDevices("all", "bytes", "", "192.168.31.10", "", 1, 10, 10)
	if err != nil {
		t.Fatalf("ListDevices() with source filter error = %v", err)
	}
	if sourceFiltered.TotalCount != 1 {
		t.Fatalf("expected source-filtered device count 1, got %d", sourceFiltered.TotalCount)
	}
	if len(sourceFiltered.Devices) != 1 || sourceFiltered.Devices[0].SourceIP != "192.168.31.10" {
		t.Fatalf("unexpected source-filtered devices: %#v", sourceFiltered.Devices)
	}
	if sourceFiltered.TotalBytes != 6144 {
		t.Fatalf("expected source-filtered bytes 6144, got %d", sourceFiltered.TotalBytes)
	}

	secondPage, err := store.ListDevices("all", "bytes", "", "", "", 2, 1, 1)
	if err != nil {
		t.Fatalf("ListDevices() page 2 error = %v", err)
	}
	if secondPage.TotalCount != 2 {
		t.Fatalf("expected total device count on page 2 to stay 2, got %d", secondPage.TotalCount)
	}
	if len(secondPage.Devices) != 1 || secondPage.Devices[0].SourceIP != "192.168.31.10" {
		t.Fatalf("unexpected second page devices: %#v", secondPage.Devices)
	}

	ascending, err := store.ListDevices("all", "bytes", "asc", "", "", 1, 10, 10)
	if err != nil {
		t.Fatalf("ListDevices() ascending bytes error = %v", err)
	}
	if len(ascending.Devices) != 2 {
		t.Fatalf("expected 2 ascending devices, got %d", len(ascending.Devices))
	}
	if ascending.Devices[0].SourceIP != "192.168.31.10" || ascending.Devices[1].SourceIP != "192.168.31.20" {
		t.Fatalf("unexpected ascending devices: %#v", ascending.Devices)
	}
}

func TestSiteTrafficStoreListDevicesCoversSortScopeSearchSourcePagesAndSiteLimit(t *testing.T) {
	store := newSiteTrafficStore(openSiteTrafficTestDB(t))
	seedTrafficMatrix(t, store)

	tests := []struct {
		name    string
		scope   string
		sortBy  string
		order   string
		source  string
		search  string
		page    int
		size    int
		limit   int
		total   int
		bytes   uint64
		devices []string
	}{
		{
			name:    "bytes desc default",
			sortBy:  "bytes",
			page:    1,
			size:    10,
			limit:   5,
			total:   3,
			bytes:   10000,
			devices: []string{"192.168.31.20", "192.168.31.10", "192.168.31.30"},
		},
		{
			name:    "bytes asc",
			sortBy:  "bytes",
			order:   "asc",
			page:    1,
			size:    10,
			limit:   5,
			total:   3,
			bytes:   10000,
			devices: []string{"192.168.31.30", "192.168.31.10", "192.168.31.20"},
		},
		{
			name:    "packets desc",
			sortBy:  "packets",
			order:   "desc",
			page:    1,
			size:    10,
			limit:   5,
			total:   3,
			bytes:   10000,
			devices: []string{"192.168.31.20", "192.168.31.10", "192.168.31.30"},
		},
		{
			name:    "name asc",
			sortBy:  "name",
			order:   "asc",
			page:    1,
			size:    10,
			limit:   5,
			total:   3,
			bytes:   10000,
			devices: []string{"192.168.31.10", "192.168.31.20", "192.168.31.30"},
		},
		{
			name:    "updated desc",
			sortBy:  "updated",
			order:   "desc",
			page:    1,
			size:    10,
			limit:   5,
			total:   3,
			bytes:   10000,
			devices: []string{"192.168.31.20", "192.168.31.30", "192.168.31.10"},
		},
		{
			name:    "tunneled scope",
			scope:   "tunneled",
			sortBy:  "name",
			order:   "asc",
			page:    1,
			size:    10,
			limit:   5,
			total:   2,
			bytes:   5000,
			devices: []string{"192.168.31.10", "192.168.31.30"},
		},
		{
			name:    "direct scope",
			scope:   "direct",
			sortBy:  "bytes",
			page:    1,
			size:    10,
			limit:   5,
			total:   1,
			bytes:   5000,
			devices: []string{"192.168.31.20"},
		},
		{
			name:    "source ip filter",
			source:  "192.168.31.20",
			sortBy:  "bytes",
			page:    1,
			size:    10,
			limit:   5,
			total:   1,
			bytes:   5000,
			devices: []string{"192.168.31.20"},
		},
		{
			name:    "device name search",
			search:  "laptop",
			sortBy:  "bytes",
			page:    1,
			size:    10,
			limit:   5,
			total:   1,
			bytes:   5000,
			devices: []string{"192.168.31.20"},
		},
		{
			name:    "source ip search",
			search:  "31.30",
			sortBy:  "bytes",
			page:    1,
			size:    10,
			limit:   5,
			total:   1,
			bytes:   2000,
			devices: []string{"192.168.31.30"},
		},
		{
			name:    "mac search",
			search:  "ee:10",
			sortBy:  "bytes",
			page:    1,
			size:    10,
			limit:   5,
			total:   1,
			bytes:   3000,
			devices: []string{"192.168.31.10"},
		},
		{
			name:    "site domain search",
			search:  "delta",
			sortBy:  "bytes",
			page:    1,
			size:    10,
			limit:   5,
			total:   1,
			bytes:   5000,
			devices: []string{"192.168.31.20"},
		},
		{
			name:    "page two",
			sortBy:  "bytes",
			page:    2,
			size:    2,
			limit:   5,
			total:   3,
			bytes:   10000,
			devices: []string{"192.168.31.30"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := store.ListDevices(tt.scope, tt.sortBy, tt.order, tt.source, tt.search, tt.page, tt.size, tt.limit)
			if err != nil {
				t.Fatalf("ListDevices() error = %v", err)
			}
			if result.TotalCount != tt.total {
				t.Fatalf("TotalCount = %d, want %d", result.TotalCount, tt.total)
			}
			if result.TotalBytes != tt.bytes {
				t.Fatalf("TotalBytes = %d, want %d", result.TotalBytes, tt.bytes)
			}
			gotDevices := make([]string, 0, len(result.Devices))
			for _, item := range result.Devices {
				gotDevices = append(gotDevices, item.SourceIP)
			}
			if strings.Join(gotDevices, ",") != strings.Join(tt.devices, ",") {
				t.Fatalf("devices = %#v, want %#v", gotDevices, tt.devices)
			}
		})
	}

	limited, err := store.ListDevices("all", "bytes", "desc", "192.168.31.20", "", 1, 10, 1)
	if err != nil {
		t.Fatalf("ListDevices() siteLimit error = %v", err)
	}
	if len(limited.Devices) != 1 || len(limited.Devices[0].Sites) != 1 {
		t.Fatalf("expected one device with one limited site, got %#v", limited.Devices)
	}
	if limited.Devices[0].Sites[0].Domain != "delta.org" {
		t.Fatalf("expected top limited site delta.org, got %+v", limited.Devices[0].Sites[0])
	}
}

func TestSiteTrafficStoreListDevicesSupportsUpdatedSort(t *testing.T) {
	store := newSiteTrafficStore(openSiteTrafficTestDB(t))

	if err := store.UpsertConnections([]siteTrafficConnection{
		{
			Key:        "old",
			SourceIP:   "192.168.31.10",
			DeviceName: "Old Device",
			DeviceMAC:  "aa:bb:cc:dd:ee:01",
			Domain:     "old.example.com",
			LastIP:     "203.0.113.10",
			Bytes:      4096,
			Packets:    32,
			ViaTunnel:  true,
			RouteLabel: "FizzVPN / NL",
		},
	}, "2026-03-26T12:00:00Z"); err != nil {
		t.Fatalf("UpsertConnections() old device error = %v", err)
	}
	if err := store.UpsertConnections([]siteTrafficConnection{
		{
			Key:        "new",
			SourceIP:   "192.168.31.20",
			DeviceName: "New Device",
			DeviceMAC:  "aa:bb:cc:dd:ee:02",
			Domain:     "new.example.com",
			LastIP:     "203.0.113.20",
			Bytes:      1024,
			Packets:    8,
			ViaTunnel:  false,
			RouteLabel: "",
		},
	}, "2026-03-26T12:05:00Z"); err != nil {
		t.Fatalf("UpsertConnections() new device error = %v", err)
	}

	ascending, err := store.ListDevices("all", "updated", "asc", "", "", 1, 10, 10)
	if err != nil {
		t.Fatalf("ListDevices() updated asc error = %v", err)
	}
	if len(ascending.Devices) != 2 || ascending.Devices[0].SourceIP != "192.168.31.10" || ascending.Devices[1].SourceIP != "192.168.31.20" {
		t.Fatalf("unexpected updated ascending order: %#v", ascending.Devices)
	}

	descending, err := store.ListDevices("all", "updated", "desc", "", "", 1, 10, 10)
	if err != nil {
		t.Fatalf("ListDevices() updated desc error = %v", err)
	}
	if len(descending.Devices) != 2 || descending.Devices[0].SourceIP != "192.168.31.20" || descending.Devices[1].SourceIP != "192.168.31.10" {
		t.Fatalf("unexpected updated descending order: %#v", descending.Devices)
	}
}

func TestServiceDeviceTrafficHistoryCustomAggregatesDeviceBuckets(t *testing.T) {
	store := newSiteTrafficStore(openSiteTrafficTestDB(t))
	service := &Service{
		siteTraffic:               store,
		siteTrafficSampleInterval: 10 * time.Second,
	}

	if err := store.UpsertConnections([]siteTrafficConnection{
		{
			Key:        "tun-1",
			SourceIP:   "192.168.31.10",
			DeviceName: "Galaxy-S25-Ultra",
			DeviceMAC:  "aa:bb:cc:dd:ee:01",
			Domain:     "openai.com",
			LastIP:     "104.18.33.45",
			Bytes:      1000,
			Packets:    10,
			ViaTunnel:  true,
			RouteLabel: "FizzVPN / NL",
		},
	}, "2026-03-26T12:00:10Z"); err != nil {
		t.Fatalf("UpsertConnections() initial error = %v", err)
	}

	if err := store.UpsertConnections([]siteTrafficConnection{
		{
			Key:        "tun-1",
			SourceIP:   "192.168.31.10",
			DeviceName: "Galaxy-S25-Ultra",
			DeviceMAC:  "aa:bb:cc:dd:ee:01",
			Domain:     "openai.com",
			LastIP:     "104.18.33.45",
			Bytes:      1600,
			Packets:    16,
			ViaTunnel:  true,
			RouteLabel: "FizzVPN / NL",
		},
	}, "2026-03-26T12:00:40Z"); err != nil {
		t.Fatalf("UpsertConnections() same-minute error = %v", err)
	}

	if err := store.UpsertConnections([]siteTrafficConnection{
		{
			Key:        "tun-1",
			SourceIP:   "192.168.31.10",
			DeviceName: "Galaxy-S25-Ultra",
			DeviceMAC:  "aa:bb:cc:dd:ee:01",
			Domain:     "openai.com",
			LastIP:     "104.18.33.45",
			Bytes:      2200,
			Packets:    22,
			ViaTunnel:  true,
			RouteLabel: "FizzVPN / NL",
		},
		{
			Key:        "direct-1",
			SourceIP:   "192.168.31.10",
			DeviceName: "Galaxy-S25-Ultra",
			DeviceMAC:  "aa:bb:cc:dd:ee:01",
			Domain:     "youtube.com",
			LastIP:     "142.250.185.206",
			Bytes:      500,
			Packets:    5,
			ViaTunnel:  false,
			RouteLabel: "",
		},
	}, "2026-03-26T12:01:10Z"); err != nil {
		t.Fatalf("UpsertConnections() next-minute error = %v", err)
	}

	history, err := service.DeviceTrafficHistoryCustom("192.168.31.10", "2026-03-26T12:00:00Z", "2026-03-26T12:02:00Z")
	if err != nil {
		t.Fatalf("DeviceTrafficHistoryCustom() error = %v", err)
	}

	if history.SourceIP != "192.168.31.10" {
		t.Fatalf("expected source ip 192.168.31.10, got %q", history.SourceIP)
	}
	if history.DeviceName != "Galaxy-S25-Ultra" {
		t.Fatalf("expected device name Galaxy-S25-Ultra, got %q", history.DeviceName)
	}
	if history.TotalBytes != 2700 {
		t.Fatalf("expected total bytes 2700, got %d", history.TotalBytes)
	}
	if history.TotalPackets != 27 {
		t.Fatalf("expected total packets 27, got %d", history.TotalPackets)
	}
	if history.TunneledBytes != 2200 {
		t.Fatalf("expected tunneled bytes 2200, got %d", history.TunneledBytes)
	}
	if history.DirectBytes != 500 {
		t.Fatalf("expected direct bytes 500, got %d", history.DirectBytes)
	}
	if history.PeakBytes != 1600 {
		t.Fatalf("expected peak bytes 1600, got %d", history.PeakBytes)
	}
	if history.BucketSeconds != 60 {
		t.Fatalf("expected bucket seconds 60, got %d", history.BucketSeconds)
	}
	if len(history.Points) != 3 {
		t.Fatalf("expected 3 points, got %d", len(history.Points))
	}

	if history.Points[0].Bytes != 1600 || history.Points[0].TunneledBytes != 1600 || history.Points[0].DirectBytes != 0 {
		t.Fatalf("unexpected first bucket: %+v", history.Points[0])
	}
	if history.Points[1].Bytes != 1100 || history.Points[1].TunneledBytes != 600 || history.Points[1].DirectBytes != 500 {
		t.Fatalf("unexpected second bucket: %+v", history.Points[1])
	}
	if history.Points[2].Bytes != 0 || history.Points[2].Packets != 0 {
		t.Fatalf("unexpected trailing bucket: %+v", history.Points[2])
	}
}

func TestSiteTrafficStoreListHistoryAggregatesDeviceDomainsInRange(t *testing.T) {
	store := newSiteTrafficStore(openSiteTrafficTestDB(t))

	if err := store.UpsertConnections([]siteTrafficConnection{
		{
			Key:        "tun-1",
			SourceIP:   "192.168.31.10",
			DeviceName: "Galaxy-S25-Ultra",
			DeviceMAC:  "aa:bb:cc:dd:ee:01",
			Domain:     "openai.com",
			LastIP:     "104.18.33.45",
			Bytes:      1000,
			Packets:    10,
			ViaTunnel:  true,
			RouteLabel: "FizzVPN / NL",
		},
	}, "2026-03-26T12:00:10Z"); err != nil {
		t.Fatalf("UpsertConnections() initial error = %v", err)
	}

	if err := store.UpsertConnections([]siteTrafficConnection{
		{
			Key:        "tun-1",
			SourceIP:   "192.168.31.10",
			DeviceName: "Galaxy-S25-Ultra",
			DeviceMAC:  "aa:bb:cc:dd:ee:01",
			Domain:     "openai.com",
			LastIP:     "104.18.33.45",
			Bytes:      2200,
			Packets:    22,
			ViaTunnel:  true,
			RouteLabel: "FizzVPN / NL",
		},
		{
			Key:        "direct-1",
			SourceIP:   "192.168.31.10",
			DeviceName: "Galaxy-S25-Ultra",
			DeviceMAC:  "aa:bb:cc:dd:ee:01",
			Domain:     "youtube.com",
			LastIP:     "142.250.185.206",
			Bytes:      500,
			Packets:    5,
			ViaTunnel:  false,
			RouteLabel: "",
		},
	}, "2026-03-26T12:01:10Z"); err != nil {
		t.Fatalf("UpsertConnections() second error = %v", err)
	}

	result, err := store.ListHistory(
		"all",
		"bytes",
		"",
		"192.168.31.10",
		"",
		1,
		10,
		time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC),
		time.Date(2026, time.March, 26, 12, 2, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("ListHistory() error = %v", err)
	}

	if result.TotalCount != 2 {
		t.Fatalf("expected 2 domains in range, got %d", result.TotalCount)
	}
	if result.TotalBytes != 2700 {
		t.Fatalf("expected 2700 bytes in range, got %d", result.TotalBytes)
	}
	if len(result.Stats) != 2 {
		t.Fatalf("expected 2 paged stats, got %d", len(result.Stats))
	}
	if result.Stats[0].Domain != "openai.com" || result.Stats[0].Bytes != 2200 || !result.Stats[0].ViaTunnel {
		t.Fatalf("unexpected first history stat: %+v", result.Stats[0])
	}
	if result.Stats[1].Domain != "youtube.com" || result.Stats[1].Bytes != 500 || result.Stats[1].ViaTunnel {
		t.Fatalf("unexpected second history stat: %+v", result.Stats[1])
	}
}

func TestSiteTrafficStoreListHistorySupportsFiltersSortingAndPagination(t *testing.T) {
	store := newSiteTrafficStore(openSiteTrafficTestDB(t))

	if err := store.UpsertConnections([]siteTrafficConnection{
		{
			Key:        "alpha",
			SourceIP:   "192.168.31.10",
			DeviceName: "Alpha Phone",
			DeviceMAC:  "aa:bb:cc:dd:ee:10",
			Domain:     "alpha.com",
			LastIP:     "203.0.113.10",
			Bytes:      1000,
			Packets:    10,
			ViaTunnel:  true,
			RouteLabel: "FizzVPN / NL",
		},
	}, "2026-03-26T12:00:00Z"); err != nil {
		t.Fatalf("UpsertConnections() alpha error = %v", err)
	}
	if err := store.UpsertConnections([]siteTrafficConnection{
		{
			Key:        "beta",
			SourceIP:   "192.168.31.10",
			DeviceName: "Alpha Phone",
			DeviceMAC:  "aa:bb:cc:dd:ee:10",
			Domain:     "beta.com",
			LastIP:     "198.51.100.20",
			Bytes:      2000,
			Packets:    20,
			ViaTunnel:  false,
			RouteLabel: "",
		},
	}, "2026-03-26T12:01:00Z"); err != nil {
		t.Fatalf("UpsertConnections() beta error = %v", err)
	}
	if err := store.UpsertConnections([]siteTrafficConnection{
		{
			Key:        "gamma",
			SourceIP:   "192.168.31.10",
			DeviceName: "Alpha Phone",
			DeviceMAC:  "aa:bb:cc:dd:ee:10",
			Domain:     "gamma.net",
			LastIP:     "203.0.113.30",
			Bytes:      1500,
			Packets:    30,
			ViaTunnel:  true,
			RouteLabel: "FizzVPN / DE",
		},
	}, "2026-03-26T12:02:00Z"); err != nil {
		t.Fatalf("UpsertConnections() gamma error = %v", err)
	}

	from := time.Date(2026, time.March, 26, 12, 0, 0, 0, time.UTC)
	to := time.Date(2026, time.March, 26, 12, 3, 0, 0, time.UTC)
	result, err := store.ListHistory("tunneled", "domain", "desc", "192.168.31.10", "", 1, 1, from, to)
	if err != nil {
		t.Fatalf("ListHistory() tunneled error = %v", err)
	}
	if result.TotalCount != 2 || result.TotalBytes != 2500 {
		t.Fatalf("unexpected tunneled history totals: %+v", result)
	}
	if len(result.Stats) != 1 || result.Stats[0].Domain != "gamma.net" {
		t.Fatalf("unexpected first tunneled history page: %+v", result.Stats)
	}

	searched, err := store.ListHistory("direct", "packets", "asc", "192.168.31.10", "198.51.100.20", 1, 10, from, to)
	if err != nil {
		t.Fatalf("ListHistory() direct search error = %v", err)
	}
	if searched.TotalCount != 1 || searched.TotalBytes != 2000 {
		t.Fatalf("unexpected direct search totals: %+v", searched)
	}
	if len(searched.Stats) != 1 || searched.Stats[0].Domain != "beta.com" || searched.Stats[0].ViaTunnel {
		t.Fatalf("unexpected direct search history stat: %+v", searched.Stats)
	}
}

func TestBuildRouteMatcherResolvesSuffixesAndKeepsRulePriority(t *testing.T) {
	state := config.State{
		Providers: []config.Provider{
			{
				ID:      "provider-sub",
				Name:    "FizzVPN",
				Type:    config.ProviderTypeSubscription,
				Source:  "https://example.com/sub",
				Enabled: true,
			},
			{
				ID:      "provider-openvpn",
				Name:    "OpenVPN",
				Type:    config.ProviderTypeOpenVPN,
				Source:  "profiles/demo.ovpn",
				Enabled: true,
			},
		},
		Rules: []config.Rule{
			{
				ID:               "rule-1",
				Name:             "Generic media",
				ProviderID:       "provider-sub",
				SelectedLocation: "NL",
				Domains:          []string{"googlevideo.com"},
				Enabled:          true,
			},
			{
				ID:         "rule-2",
				Name:       "Specific media",
				ProviderID: "provider-openvpn",
				Domains:    []string{"rr2---sn-a5mekn7z.googlevideo.com"},
				Enabled:    true,
			},
		},
		Routing: config.RoutingSettings{
			VPNIface: "tun0",
		},
	}

	matcher := buildRouteMatcher(state)

	viaTunnel, routeLabel := matcher.resolve("rr2---sn-a5mekn7z.googlevideo.com")
	if !viaTunnel {
		t.Fatalf("expected matched domain to be routed")
	}
	if routeLabel != "FizzVPN / NL" {
		t.Fatalf("expected first matching rule to win, got %q", routeLabel)
	}

	viaTunnel, routeLabel = matcher.resolve("rr2---sn-a5mekn7z.googlevideo.com.")
	if !viaTunnel || routeLabel != "FizzVPN / NL" {
		t.Fatalf("expected normalized observed domain to resolve, got routed=%t label=%q", viaTunnel, routeLabel)
	}
}

func TestBuildRouteMatcherIgnoresDisabledRulesAndProviders(t *testing.T) {
	state := config.State{
		Providers: []config.Provider{
			{
				ID:      "provider-disabled",
				Name:    "Disabled",
				Type:    config.ProviderTypeSubscription,
				Source:  "https://example.com/sub",
				Enabled: false,
			},
			{
				ID:      "provider-enabled",
				Name:    "Enabled",
				Type:    config.ProviderTypeOpenVPN,
				Source:  "profiles/demo.ovpn",
				Enabled: true,
			},
		},
		Rules: []config.Rule{
			{
				ID:               "rule-disabled-provider",
				Name:             "Disabled provider",
				ProviderID:       "provider-disabled",
				SelectedLocation: "US",
				Domains:          []string{"openai.com"},
				Enabled:          true,
			},
			{
				ID:         "rule-disabled",
				Name:       "Disabled rule",
				ProviderID: "provider-enabled",
				Domains:    []string{"chatgpt.com"},
				Enabled:    false,
			},
			{
				ID:         "rule-enabled",
				Name:       "Enabled rule",
				ProviderID: "provider-enabled",
				Domains:    []string{"oaistatic.com"},
				Enabled:    true,
			},
		},
		Routing: config.RoutingSettings{
			VPNIface: "tun9",
		},
	}

	matcher := buildRouteMatcher(state)

	if viaTunnel, _ := matcher.resolve("openai.com"); viaTunnel {
		t.Fatalf("expected disabled provider route to be ignored")
	}
	if viaTunnel, _ := matcher.resolve("chatgpt.com"); viaTunnel {
		t.Fatalf("expected disabled rule route to be ignored")
	}

	viaTunnel, routeLabel := matcher.resolve("files.oaistatic.com")
	if !viaTunnel {
		t.Fatalf("expected enabled route to match suffix")
	}
	if routeLabel != "Enabled / tun9" {
		t.Fatalf("unexpected route label %q", routeLabel)
	}
}

func TestBuildRouteMatcherResolvesIPv4AndCIDREntries(t *testing.T) {
	state := config.State{
		Providers: []config.Provider{
			{
				ID:      "provider-broad",
				Name:    "Broad",
				Type:    config.ProviderTypeSubscription,
				Source:  "https://example.com/sub",
				Enabled: true,
			},
			{
				ID:      "provider-specific",
				Name:    "Specific",
				Type:    config.ProviderTypeOpenVPN,
				Source:  "profiles/demo.ovpn",
				Enabled: true,
			},
		},
		Rules: []config.Rule{
			{
				ID:               "rule-broad",
				Name:             "Broad CIDR",
				ProviderID:       "provider-broad",
				SelectedLocation: "NL",
				Domains:          []string{"149.154.160.0/20"},
				Enabled:          true,
			},
			{
				ID:         "rule-specific",
				Name:       "Specific host",
				ProviderID: "provider-specific",
				Domains:    []string{"149.154.167.41"},
				Enabled:    true,
			},
		},
		Routing: config.RoutingSettings{
			VPNIface: "tun7",
		},
	}

	matcher := buildRouteMatcher(state)

	viaTunnel, routeLabel := matcher.resolve("149.154.167.41")
	if !viaTunnel {
		t.Fatalf("expected matched ip to be routed")
	}
	if routeLabel != "Broad / NL" {
		t.Fatalf("expected first matching IP rule to win, got %q", routeLabel)
	}

	if viaTunnel, _ := matcher.resolve("149.154.200.1"); viaTunnel {
		t.Fatalf("expected unrelated ip to stay outside the route")
	}
}

func TestDNSObservationsPreferOriginalQueryDomainForCNAMEReply(t *testing.T) {
	observations := dnsObservationsFromLines([]string{
		"Wed Jul  8 12:00:00 2026 daemon.info dnsmasq[1234]: 42 192.168.31.10/5353 query[A] chat.openai.com from 192.168.31.10",
		"Wed Jul  8 12:00:00 2026 daemon.info dnsmasq[1234]: 42 192.168.31.10/5353 reply chat.openai.com is <CNAME>",
		"Wed Jul  8 12:00:00 2026 daemon.info dnsmasq[1234]: 42 192.168.31.10/5353 reply chat.openai.com.cdn.cloudflare.net is 104.18.33.45",
	}, "2026-07-08T09:00:00Z")

	if len(observations) != 1 {
		t.Fatalf("expected one observation, got %+v", observations)
	}
	if observations[0].Domain != "chat.openai.com" {
		t.Fatalf("expected original query domain, got %q", observations[0].Domain)
	}
	if observations[0].IP != "104.18.33.45" {
		t.Fatalf("expected observed IP, got %q", observations[0].IP)
	}
}

func TestDNSObservationsHandlePlainReplyWithoutExtraPrefix(t *testing.T) {
	observations := dnsObservationsFromLines([]string{
		"dnsmasq[1234]: reply youtube.com is 142.250.185.206",
	}, "2026-07-08T09:00:00Z")

	if len(observations) != 1 {
		t.Fatalf("expected one observation, got %+v", observations)
	}
	if observations[0].Domain != "youtube.com" || observations[0].IP != "142.250.185.206" {
		t.Fatalf("unexpected observation: %+v", observations[0])
	}
}

func TestOpenConntrackTableFallsBackToLegacyPath(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "ip_conntrack")
	if err := os.WriteFile(legacyPath, []byte(""), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	originalPaths := conntrackTablePaths
	conntrackTablePaths = []string{
		filepath.Join(dir, "nf_conntrack"),
		legacyPath,
	}
	t.Cleanup(func() {
		conntrackTablePaths = originalPaths
	})

	file, err := openConntrackTable()
	if err != nil {
		t.Fatalf("openConntrackTable() error = %v", err)
	}
	defer file.Close()

	if file.Name() != legacyPath {
		t.Fatalf("opened %q, want %q", file.Name(), legacyPath)
	}
}

func seedTrafficMatrix(t *testing.T, store *siteTrafficStore) {
	t.Helper()

	entries := []struct {
		now   string
		entry siteTrafficConnection
	}{
		{
			now: "2026-03-26T12:00:00Z",
			entry: siteTrafficConnection{
				Key:        "alpha",
				SourceIP:   "192.168.31.10",
				DeviceName: "Alpha Phone",
				DeviceMAC:  "aa:bb:cc:dd:ee:10",
				Domain:     "alpha.com",
				LastIP:     "203.0.113.10",
				Bytes:      3000,
				Packets:    30,
				ViaTunnel:  true,
				RouteLabel: "FizzVPN / NL",
			},
		},
		{
			now: "2026-03-26T12:01:00Z",
			entry: siteTrafficConnection{
				Key:        "beta",
				SourceIP:   "192.168.31.20",
				DeviceName: "Beta Laptop",
				DeviceMAC:  "aa:bb:cc:dd:ee:20",
				Domain:     "beta.com",
				LastIP:     "198.51.100.20",
				Bytes:      1000,
				Packets:    50,
				ViaTunnel:  false,
				RouteLabel: "",
			},
		},
		{
			now: "2026-03-26T12:02:00Z",
			entry: siteTrafficConnection{
				Key:        "gamma",
				SourceIP:   "192.168.31.30",
				DeviceName: "Gamma TV",
				DeviceMAC:  "aa:bb:cc:dd:ee:30",
				Domain:     "gamma.net",
				LastIP:     "203.0.113.30",
				Bytes:      2000,
				Packets:    10,
				ViaTunnel:  true,
				RouteLabel: "FizzVPN / DE",
			},
		},
		{
			now: "2026-03-26T12:03:00Z",
			entry: siteTrafficConnection{
				Key:        "delta",
				SourceIP:   "192.168.31.20",
				DeviceName: "Beta Laptop",
				DeviceMAC:  "aa:bb:cc:dd:ee:20",
				Domain:     "delta.org",
				LastIP:     "198.51.100.40",
				Bytes:      4000,
				Packets:    40,
				ViaTunnel:  false,
				RouteLabel: "",
			},
		},
	}

	for _, item := range entries {
		if err := store.UpsertConnections([]siteTrafficConnection{item.entry}, item.now); err != nil {
			t.Fatalf("UpsertConnections(%s) error = %v", item.entry.Domain, err)
		}
	}
}

func openSiteTrafficTestDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sqlitedb.Open(filepath.Join(t.TempDir(), "vpn-manager.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}
