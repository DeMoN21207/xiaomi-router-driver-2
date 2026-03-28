package status

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/subscription"
)

const (
	defaultTrafficSampleInterval       = 10 * time.Minute
	defaultDomainTrafficSampleInterval = 30 * time.Second
	trafficHistoryRetention            = 30 * 24 * time.Hour
)

type TrafficHistoryPoint struct {
	At         string `json:"at"`
	TotalBytes uint64 `json:"totalBytes"`
}

type TrafficHistoryRoutePoint struct {
	At         string `json:"at"`
	RXBytes    uint64 `json:"rxBytes"`
	TXBytes    uint64 `json:"txBytes"`
	TotalBytes uint64 `json:"totalBytes"`
}

type TrafficHistoryBreakdown struct {
	Key          string `json:"key"`
	ProviderID   string `json:"providerId"`
	ProviderName string `json:"providerName"`
	ProviderType string `json:"providerType"`
	Location     string `json:"location"`
	RXBytes      uint64 `json:"rxBytes"`
	TXBytes      uint64 `json:"txBytes"`
	TotalBytes   uint64 `json:"totalBytes"`
}

type TrafficHistoryRouteSeries struct {
	Key           string                     `json:"key"`
	ProviderID    string                     `json:"providerId"`
	ProviderName  string                     `json:"providerName"`
	ProviderType  string                     `json:"providerType"`
	Location      string                     `json:"location"`
	InterfaceName string                     `json:"interfaceName"`
	TotalBytes    uint64                     `json:"totalBytes"`
	PeakBytes     uint64                     `json:"peakBytes"`
	Points        []TrafficHistoryRoutePoint `json:"points"`
}

type TrafficHistoryResponse struct {
	Range                 string                      `json:"range"`
	BucketSeconds         int                         `json:"bucketSeconds"`
	SampleIntervalSeconds int                         `json:"sampleIntervalSeconds"`
	TotalBytes            uint64                      `json:"totalBytes"`
	AvailableSince        string                      `json:"availableSince"`
	LatestSampleAt        string                      `json:"latestSampleAt"`
	Points                []TrafficHistoryPoint       `json:"points"`
	Breakdown             []TrafficHistoryBreakdown   `json:"breakdown"`
	RouteSeries           []TrafficHistoryRouteSeries `json:"routeSeries"`
}

type trafficHistoryStore struct {
	db          *sql.DB
	legacyPath  string
	retention   time.Duration
	mu          sync.Mutex
	initialized bool
	initErr     error
}

type trafficHistoryFile struct {
	Samples []trafficHistorySample `json:"samples"`
}

type trafficHistorySample struct {
	CollectedAt string                    `json:"collectedAt"`
	Routes      []trafficHistoryRouteStat `json:"routes"`
}

type trafficHistoryRouteStat struct {
	ProviderID    string `json:"providerId"`
	ProviderName  string `json:"providerName"`
	ProviderType  string `json:"providerType"`
	Location      string `json:"location"`
	InterfaceName string `json:"interfaceName"`
	RXBytes       uint64 `json:"rxBytes"`
	TXBytes       uint64 `json:"txBytes"`
}

type parsedTrafficHistorySample struct {
	At     time.Time
	Routes []trafficHistoryRouteStat
}

type trafficHistoryRangeSpec struct {
	Name     string
	Lookback time.Duration
	Bucket   time.Duration
}

func newTrafficHistoryStore(db *sql.DB, legacyPath string, retention time.Duration) *trafficHistoryStore {
	return &trafficHistoryStore{
		db:         db,
		legacyPath: strings.TrimSpace(legacyPath),
		retention:  retention,
	}
}

func (s *Service) RunTrafficSampler(ctx context.Context) {
	if s.history == nil || s.trafficSampleInterval <= 0 {
		return
	}

	if err := s.SampleTrafficHistory(); err != nil {
		log.Printf("traffic sampler initial sample failed: %v", err)
	}

	ticker := time.NewTicker(s.trafficSampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.SampleTrafficHistory(); err != nil {
				log.Printf("traffic sampler sample failed: %v", err)
			}
		}
	}
}

func (s *Service) RunDomainTrafficSampler(ctx context.Context) {
	if s.domainTraffic == nil || s.domainTrafficSampleInterval <= 0 {
		return
	}

	if err := s.SampleDomainTraffic(); err != nil {
		log.Printf("domain traffic sampler initial sample failed: %v", err)
	}

	ticker := time.NewTicker(s.domainTrafficSampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.SampleDomainTraffic(); err != nil {
				log.Printf("domain traffic sample failed: %v", err)
			}
		}
	}
}

func (s *Service) SampleTrafficHistory() error {
	if s.history == nil {
		return nil
	}

	routes, err := s.TrafficRoutes()
	if err != nil {
		return err
	}

	sample := trafficHistorySample{
		CollectedAt: time.Now().UTC().Format(time.RFC3339),
		Routes:      make([]trafficHistoryRouteStat, 0, len(routes)),
	}
	for _, route := range routes {
		sample.Routes = append(sample.Routes, trafficHistoryRouteStat{
			ProviderID:    route.ProviderID,
			ProviderName:  route.ProviderName,
			ProviderType:  route.ProviderType,
			Location:      route.Location,
			InterfaceName: route.InterfaceName,
			RXBytes:       route.RXBytes,
			TXBytes:       route.TXBytes,
		})
	}

	return s.history.Append(sample)
}

func (s *Service) TrafficRoutes() ([]TrafficRoute, error) {
	state, err := s.state.Load()
	if err != nil {
		return nil, err
	}

	_, _, domainsByProvider := summarizeEnabledRules(state)
	subscriptionRuntime := []subscription.RuntimeSnapshot{}
	if s.subscriptions != nil {
		subscriptionRuntime, err = s.subscriptions.Snapshots()
		if err != nil {
			return nil, err
		}
	}

	return buildTrafficRoutes(state, subscriptionRuntime, domainsByProvider), nil
}

func (s *Service) TrafficHistory(rangeName string) (TrafficHistoryResponse, error) {
	spec, err := parseTrafficHistoryRange(rangeName)
	if err != nil {
		return TrafficHistoryResponse{}, err
	}

	if s.history == nil {
		return TrafficHistoryResponse{
			Range:                 spec.Name,
			BucketSeconds:         int(spec.Bucket.Seconds()),
			SampleIntervalSeconds: int(s.trafficSampleInterval.Seconds()),
			Points:                []TrafficHistoryPoint{},
			Breakdown:             []TrafficHistoryBreakdown{},
			RouteSeries:           []TrafficHistoryRouteSeries{},
		}, nil
	}

	samples, err := s.history.List()
	if err != nil {
		return TrafficHistoryResponse{}, err
	}

	return aggregateTrafficHistory(samples, spec, time.Now().UTC(), s.trafficSampleInterval), nil
}

func summarizeEnabledRules(state config.State) (int, map[string]int, map[string]map[string]struct{}) {
	enabledRules := 0
	rulesByProvider := make(map[string]int, len(state.Rules))
	domainsByProvider := make(map[string]map[string]struct{}, len(state.Rules))
	providersByID := make(map[string]config.Provider, len(state.Providers))
	for _, provider := range state.Providers {
		providersByID[provider.ID] = provider
	}

	for _, rule := range state.Rules {
		if !rule.Enabled {
			continue
		}
		provider, exists := providersByID[rule.ProviderID]
		if !exists || !provider.Enabled {
			continue
		}

		enabledRules++
		rulesByProvider[rule.ProviderID]++
		if domainsByProvider[rule.ProviderID] == nil {
			domainsByProvider[rule.ProviderID] = make(map[string]struct{}, len(rule.Domains))
		}
		for _, domain := range rule.Domains {
			domainsByProvider[rule.ProviderID][domain] = struct{}{}
		}
	}

	return enabledRules, rulesByProvider, domainsByProvider
}

func (s *Service) TrafficHistoryCustom(fromStr, toStr string) (TrafficHistoryResponse, error) {
	from, err := time.Parse("2006-01-02", strings.TrimSpace(fromStr))
	if err != nil {
		from, err = time.Parse(time.RFC3339, strings.TrimSpace(fromStr))
		if err != nil {
			return TrafficHistoryResponse{}, errors.New("invalid 'from' date, expected YYYY-MM-DD")
		}
	}

	to, err := time.Parse("2006-01-02", strings.TrimSpace(toStr))
	if err != nil {
		to, err = time.Parse(time.RFC3339, strings.TrimSpace(toStr))
		if err != nil {
			return TrafficHistoryResponse{}, errors.New("invalid 'to' date, expected YYYY-MM-DD")
		}
	}
	// If date-only format, set to end of day
	if len(strings.TrimSpace(toStr)) == 10 {
		to = to.Add(24*time.Hour - time.Second)
	}

	from = from.UTC()
	to = to.UTC()

	if !to.After(from) {
		return TrafficHistoryResponse{}, errors.New("'to' must be after 'from'")
	}

	duration := to.Sub(from)
	var bucket time.Duration
	switch {
	case duration <= 24*time.Hour:
		bucket = time.Hour
	case duration <= 3*24*time.Hour:
		bucket = 3 * time.Hour
	case duration <= 7*24*time.Hour:
		bucket = 6 * time.Hour
	default:
		bucket = 24 * time.Hour
	}

	spec := trafficHistoryRangeSpec{
		Name:     "custom",
		Lookback: duration,
		Bucket:   bucket,
	}

	if s.history == nil {
		return TrafficHistoryResponse{
			Range:                 spec.Name,
			BucketSeconds:         int(spec.Bucket.Seconds()),
			SampleIntervalSeconds: int(s.trafficSampleInterval.Seconds()),
			Points:                []TrafficHistoryPoint{},
			Breakdown:             []TrafficHistoryBreakdown{},
			RouteSeries:           []TrafficHistoryRouteSeries{},
		}, nil
	}

	samples, err := s.history.List()
	if err != nil {
		return TrafficHistoryResponse{}, err
	}

	return aggregateTrafficHistory(samples, spec, to, s.trafficSampleInterval), nil
}

func parseTrafficHistoryRange(raw string) (trafficHistoryRangeSpec, error) {
	switch strings.TrimSpace(raw) {
	case "", "7d":
		return trafficHistoryRangeSpec{Name: "7d", Lookback: 7 * 24 * time.Hour, Bucket: 6 * time.Hour}, nil
	case "1h":
		return trafficHistoryRangeSpec{Name: "1h", Lookback: time.Hour, Bucket: 5 * time.Minute}, nil
	case "3h":
		return trafficHistoryRangeSpec{Name: "3h", Lookback: 3 * time.Hour, Bucket: 15 * time.Minute}, nil
	case "1d":
		return trafficHistoryRangeSpec{Name: "1d", Lookback: 24 * time.Hour, Bucket: time.Hour}, nil
	case "3d":
		return trafficHistoryRangeSpec{Name: "3d", Lookback: 3 * 24 * time.Hour, Bucket: 3 * time.Hour}, nil
	case "30d":
		return trafficHistoryRangeSpec{Name: "30d", Lookback: 30 * 24 * time.Hour, Bucket: 24 * time.Hour}, nil
	default:
		return trafficHistoryRangeSpec{}, errors.New("unsupported traffic history range")
	}
}

func aggregateTrafficHistory(samples []trafficHistorySample, spec trafficHistoryRangeSpec, now time.Time, sampleInterval time.Duration) TrafficHistoryResponse {
	response := TrafficHistoryResponse{
		Range:                 spec.Name,
		BucketSeconds:         int(spec.Bucket.Seconds()),
		SampleIntervalSeconds: int(sampleInterval.Seconds()),
		Points:                []TrafficHistoryPoint{},
		Breakdown:             []TrafficHistoryBreakdown{},
		RouteSeries:           []TrafficHistoryRouteSeries{},
	}

	parsed := make([]parsedTrafficHistorySample, 0, len(samples))
	for _, sample := range samples {
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(sample.CollectedAt))
		if err != nil {
			continue
		}
		parsed = append(parsed, parsedTrafficHistorySample{
			At:     at.UTC(),
			Routes: sample.Routes,
		})
	}
	if len(parsed) == 0 {
		return response
	}

	sort.Slice(parsed, func(i, j int) bool {
		return parsed[i].At.Before(parsed[j].At)
	})

	response.AvailableSince = parsed[0].At.Format(time.RFC3339)
	response.LatestSampleAt = parsed[len(parsed)-1].At.Format(time.RFC3339)

	start := now.Add(-spec.Lookback)
	bucketStart := start.Truncate(spec.Bucket)
	for at := bucketStart; !at.After(now); at = at.Add(spec.Bucket) {
		response.Points = append(response.Points, TrafficHistoryPoint{
			At: at.Format(time.RFC3339),
		})
	}

	pointIndex := make(map[int64]int, len(response.Points))
	for index, point := range response.Points {
		at, err := time.Parse(time.RFC3339, point.At)
		if err != nil {
			continue
		}
		pointIndex[at.Unix()] = index
	}

	breakdown := make(map[string]*TrafficHistoryBreakdown)
	seriesByKey := make(map[string]*TrafficHistoryRouteSeries)
	for index := 1; index < len(parsed); index++ {
		prev := parsed[index-1]
		curr := parsed[index]
		if curr.At.Before(start) {
			continue
		}

		bucketAt := curr.At.Truncate(spec.Bucket)
		pointPosition, exists := pointIndex[bucketAt.Unix()]
		if !exists {
			continue
		}

		prevByKey := make(map[string]trafficHistoryRouteStat, len(prev.Routes))
		for _, route := range prev.Routes {
			prevByKey[trafficRouteHistoryKey(route)] = route
		}

		for _, route := range curr.Routes {
			key := trafficRouteHistoryKey(route)
			prevRoute, exists := prevByKey[key]
			if !exists {
				continue
			}

			deltaRX := counterDelta(route.RXBytes, prevRoute.RXBytes)
			deltaTX := counterDelta(route.TXBytes, prevRoute.TXBytes)
			totalDelta := deltaRX + deltaTX
			if totalDelta == 0 {
				continue
			}

			response.Points[pointPosition].TotalBytes += totalDelta
			response.TotalBytes += totalDelta

			item := breakdown[key]
			if item == nil {
				item = &TrafficHistoryBreakdown{
					Key:          key,
					ProviderID:   route.ProviderID,
					ProviderName: route.ProviderName,
					ProviderType: route.ProviderType,
					Location:     route.Location,
				}
				breakdown[key] = item
			}
			item.RXBytes += deltaRX
			item.TXBytes += deltaTX
			item.TotalBytes += totalDelta

			series := seriesByKey[key]
			if series == nil {
				series = &TrafficHistoryRouteSeries{
					Key:           key,
					ProviderID:    route.ProviderID,
					ProviderName:  route.ProviderName,
					ProviderType:  route.ProviderType,
					Location:      route.Location,
					InterfaceName: route.InterfaceName,
					Points:        make([]TrafficHistoryRoutePoint, len(response.Points)),
				}
				for idx, point := range response.Points {
					series.Points[idx] = TrafficHistoryRoutePoint{At: point.At}
				}
				seriesByKey[key] = series
			}

			series.Points[pointPosition].RXBytes += deltaRX
			series.Points[pointPosition].TXBytes += deltaTX
			series.Points[pointPosition].TotalBytes += totalDelta
			series.TotalBytes += totalDelta
			if series.Points[pointPosition].TotalBytes > series.PeakBytes {
				series.PeakBytes = series.Points[pointPosition].TotalBytes
			}
		}
	}

	response.Breakdown = make([]TrafficHistoryBreakdown, 0, len(breakdown))
	for _, item := range breakdown {
		response.Breakdown = append(response.Breakdown, *item)
	}
	sort.Slice(response.Breakdown, func(i, j int) bool {
		if response.Breakdown[i].TotalBytes == response.Breakdown[j].TotalBytes {
			if response.Breakdown[i].ProviderName == response.Breakdown[j].ProviderName {
				return response.Breakdown[i].Location < response.Breakdown[j].Location
			}
			return response.Breakdown[i].ProviderName < response.Breakdown[j].ProviderName
		}
		return response.Breakdown[i].TotalBytes > response.Breakdown[j].TotalBytes
	})

	response.RouteSeries = make([]TrafficHistoryRouteSeries, 0, len(seriesByKey))
	for _, item := range seriesByKey {
		response.RouteSeries = append(response.RouteSeries, *item)
	}
	sort.Slice(response.RouteSeries, func(i, j int) bool {
		if response.RouteSeries[i].TotalBytes == response.RouteSeries[j].TotalBytes {
			if response.RouteSeries[i].ProviderName == response.RouteSeries[j].ProviderName {
				return response.RouteSeries[i].Location < response.RouteSeries[j].Location
			}
			return response.RouteSeries[i].ProviderName < response.RouteSeries[j].ProviderName
		}
		return response.RouteSeries[i].TotalBytes > response.RouteSeries[j].TotalBytes
	})

	return response
}

func counterDelta(current uint64, previous uint64) uint64 {
	if current >= previous {
		return current - previous
	}
	return current
}

func trafficRouteHistoryKey(route trafficHistoryRouteStat) string {
	return strings.Join([]string{
		firstNonEmpty(route.ProviderID, route.ProviderName),
		firstNonEmpty(route.Location, route.InterfaceName),
		route.InterfaceName,
	}, "|")
}

func resolveTrafficSampleInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("VPN_MANAGER_TRAFFIC_SAMPLE_INTERVAL"))
	if raw == "" {
		return defaultTrafficSampleInterval
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < time.Minute {
		return defaultTrafficSampleInterval
	}

	return parsed
}

func resolveDomainTrafficSampleInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("VPN_MANAGER_DOMAIN_TRAFFIC_SAMPLE_INTERVAL"))
	if raw == "" {
		return defaultDomainTrafficSampleInterval
	}

	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed < 5*time.Second {
		return defaultDomainTrafficSampleInterval
	}

	return parsed
}

func (s *trafficHistoryStore) Append(sample trafficHistorySample) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureReadyLocked(); err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	if err := insertTrafficHistorySampleTx(tx, sample); err != nil {
		_ = tx.Rollback()
		return err
	}

	if err := pruneTrafficHistoryTx(tx, time.Now().UTC().Add(-s.retention)); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

func (s *trafficHistoryStore) List() ([]trafficHistorySample, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureReadyLocked(); err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`
		SELECT
			s.id,
			s.collected_at,
			r.provider_id,
			r.provider_name,
			r.provider_type,
			r.location,
			r.interface_name,
			r.rx_bytes,
			r.tx_bytes
		FROM traffic_history_samples s
		LEFT JOIN traffic_history_routes r ON r.sample_id = s.id
		ORDER BY s.collected_at ASC, s.id ASC, r.provider_name ASC, r.location ASC, r.interface_name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	samples := make([]trafficHistorySample, 0, 64)
	var currentSampleID int64 = -1

	for rows.Next() {
		var sampleID int64
		var collectedAt string
		var providerID, providerName, providerType, location, interfaceName sql.NullString
		var rxBytes, txBytes sql.NullInt64

		if err := rows.Scan(&sampleID, &collectedAt, &providerID, &providerName, &providerType, &location, &interfaceName, &rxBytes, &txBytes); err != nil {
			return nil, err
		}

		if sampleID != currentSampleID {
			samples = append(samples, trafficHistorySample{
				CollectedAt: collectedAt,
				Routes:      []trafficHistoryRouteStat{},
			})
			currentSampleID = sampleID
		}

		if providerID.Valid {
			samples[len(samples)-1].Routes = append(samples[len(samples)-1].Routes, trafficHistoryRouteStat{
				ProviderID:    providerID.String,
				ProviderName:  providerName.String,
				ProviderType:  providerType.String,
				Location:      location.String,
				InterfaceName: interfaceName.String,
				RXBytes:       int64ToUint64(rxBytes.Int64),
				TXBytes:       int64ToUint64(txBytes.Int64),
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return samples, nil
}

func (s *trafficHistoryStore) ensureReadyLocked() error {
	if s.initialized {
		return s.initErr
	}
	s.initialized = true

	if s.db == nil {
		s.initErr = errors.New("traffic history database is not configured")
		return s.initErr
	}

	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS traffic_history_samples (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			collected_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS traffic_history_routes (
			sample_id INTEGER NOT NULL,
			provider_id TEXT NOT NULL,
			provider_name TEXT NOT NULL,
			provider_type TEXT NOT NULL,
			location TEXT NOT NULL,
			interface_name TEXT NOT NULL,
			rx_bytes INTEGER NOT NULL,
			tx_bytes INTEGER NOT NULL,
			PRIMARY KEY (sample_id, provider_id, location, interface_name),
			FOREIGN KEY (sample_id) REFERENCES traffic_history_samples(id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS idx_traffic_history_samples_collected_at ON traffic_history_samples(collected_at)`,
		`CREATE INDEX IF NOT EXISTS idx_traffic_history_routes_sample_id ON traffic_history_routes(sample_id)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			s.initErr = err
			return err
		}
	}

	if err := s.migrateLegacyLocked(); err != nil {
		s.initErr = err
		return err
	}

	return nil
}

func (s *trafficHistoryStore) migrateLegacyLocked() error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(1) FROM traffic_history_samples`).Scan(&count); err != nil || count > 0 {
		return err
	}

	history, err := loadLegacyTrafficHistory(s.legacyPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}

	for _, sample := range history.Samples {
		if err := insertTrafficHistorySampleTx(tx, sample); err != nil {
			_ = tx.Rollback()
			return err
		}
	}

	if err := pruneTrafficHistoryTx(tx, time.Now().UTC().Add(-s.retention)); err != nil {
		_ = tx.Rollback()
		return err
	}

	return tx.Commit()
}

func insertTrafficHistorySampleTx(tx *sql.Tx, sample trafficHistorySample) error {
	result, err := tx.Exec(`
		INSERT INTO traffic_history_samples (collected_at)
		VALUES (?)
	`, sample.CollectedAt)
	if err != nil {
		return err
	}

	sampleID, err := result.LastInsertId()
	if err != nil {
		return err
	}

	for _, route := range sample.Routes {
		if _, err := tx.Exec(`
			INSERT INTO traffic_history_routes (
				sample_id, provider_id, provider_name, provider_type, location, interface_name, rx_bytes, tx_bytes
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, sampleID, route.ProviderID, route.ProviderName, route.ProviderType, route.Location, route.InterfaceName, uint64ToInt64(route.RXBytes), uint64ToInt64(route.TXBytes)); err != nil {
			return err
		}
	}

	return nil
}

func pruneTrafficHistoryTx(tx *sql.Tx, cutoff time.Time) error {
	_, err := tx.Exec(`
		DELETE FROM traffic_history_samples
		WHERE collected_at < ?
	`, cutoff.Format(time.RFC3339))
	return err
}

func loadLegacyTrafficHistory(path string) (trafficHistoryFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return trafficHistoryFile{}, err
	}

	if len(strings.TrimSpace(string(data))) == 0 {
		return trafficHistoryFile{Samples: []trafficHistorySample{}}, nil
	}

	var history trafficHistoryFile
	if err := json.Unmarshal(data, &history); err != nil {
		return trafficHistoryFile{}, err
	}
	if history.Samples == nil {
		history.Samples = []trafficHistorySample{}
	}

	return history, nil
}

func uint64ToInt64(value uint64) int64 {
	if value > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(value)
}

func int64ToUint64(value int64) uint64 {
	if value < 0 {
		return 0
	}
	return uint64(value)
}
