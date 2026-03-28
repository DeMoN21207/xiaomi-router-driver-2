package status

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

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

	result, err := store.List("all", "bytes", "", "", 1, 2)
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

	filtered, err := store.List("tunneled", "domain", "192.168.31.10", "", 1, 10)
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

	result, err := store.ListDevices("all", "bytes", "", "", 1, 1, 1)
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

	filtered, err := store.ListDevices("tunneled", "name", "", "chatgpt", 1, 10, 10)
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

	sourceFiltered, err := store.ListDevices("all", "bytes", "192.168.31.10", "", 1, 10, 10)
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
