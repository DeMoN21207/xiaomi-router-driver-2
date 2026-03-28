package status

import (
	"errors"
	"sort"
	"strings"
	"time"
)

type DeviceTrafficHistoryPoint struct {
	At            string `json:"at"`
	Bytes         uint64 `json:"bytes"`
	Packets       uint64 `json:"packets"`
	TunneledBytes uint64 `json:"tunneledBytes"`
	DirectBytes   uint64 `json:"directBytes"`
}

type DeviceTrafficHistoryResponse struct {
	SourceIP              string                      `json:"sourceIp"`
	DeviceName            string                      `json:"deviceName"`
	DeviceMAC             string                      `json:"deviceMac"`
	Range                 string                      `json:"range"`
	BucketSeconds         int                         `json:"bucketSeconds"`
	SampleIntervalSeconds int                         `json:"sampleIntervalSeconds"`
	TotalBytes            uint64                      `json:"totalBytes"`
	TotalPackets          uint64                      `json:"totalPackets"`
	TunneledBytes         uint64                      `json:"tunneledBytes"`
	DirectBytes           uint64                      `json:"directBytes"`
	PeakBytes             uint64                      `json:"peakBytes"`
	AvailableSince        string                      `json:"availableSince"`
	LatestSampleAt        string                      `json:"latestSampleAt"`
	Points                []DeviceTrafficHistoryPoint `json:"points"`
}

type deviceTrafficHistorySample struct {
	At            time.Time
	SourceIP      string
	DeviceName    string
	DeviceMAC     string
	Bytes         uint64
	Packets       uint64
	TunneledBytes uint64
	DirectBytes   uint64
}

func (s *Service) DeviceTrafficHistory(sourceIP string, rangeName string) (DeviceTrafficHistoryResponse, error) {
	spec, err := parseDeviceTrafficHistoryRange(rangeName)
	if err != nil {
		return DeviceTrafficHistoryResponse{}, err
	}

	now := time.Now().UTC()
	return s.deviceTrafficHistoryWindow(strings.TrimSpace(sourceIP), spec, now.Add(-spec.Lookback), now)
}

func (s *Service) DeviceTrafficHistoryCustom(sourceIP string, fromStr string, toStr string) (DeviceTrafficHistoryResponse, error) {
	spec, from, to, err := parseDeviceTrafficHistoryCustomRange(fromStr, toStr)
	if err != nil {
		return DeviceTrafficHistoryResponse{}, err
	}

	return s.deviceTrafficHistoryWindow(strings.TrimSpace(sourceIP), spec, from, to)
}

func (s *Service) deviceTrafficHistoryWindow(sourceIP string, spec trafficHistoryRangeSpec, from time.Time, to time.Time) (DeviceTrafficHistoryResponse, error) {
	if sourceIP == "" {
		return DeviceTrafficHistoryResponse{}, errors.New("sourceIp is required")
	}

	response := DeviceTrafficHistoryResponse{
		SourceIP:              sourceIP,
		Range:                 spec.Name,
		BucketSeconds:         int(spec.Bucket.Seconds()),
		SampleIntervalSeconds: int(s.siteTrafficSampleInterval.Seconds()),
		Points:                make([]DeviceTrafficHistoryPoint, 0),
	}
	if s.siteTraffic == nil {
		return response, nil
	}

	samples, err := s.siteTraffic.ListDeviceHistory(sourceIP, from, to)
	if err != nil {
		return DeviceTrafficHistoryResponse{}, err
	}

	return aggregateDeviceTrafficHistory(samples, spec, from, to, s.siteTrafficSampleInterval, sourceIP), nil
}

func parseDeviceTrafficHistoryRange(raw string) (trafficHistoryRangeSpec, error) {
	switch strings.TrimSpace(raw) {
	case "", "1h":
		return trafficHistoryRangeSpec{Name: "1h", Lookback: time.Hour, Bucket: time.Minute}, nil
	case "3h":
		return trafficHistoryRangeSpec{Name: "3h", Lookback: 3 * time.Hour, Bucket: 5 * time.Minute}, nil
	case "1d":
		return trafficHistoryRangeSpec{Name: "1d", Lookback: 24 * time.Hour, Bucket: 30 * time.Minute}, nil
	case "3d":
		return trafficHistoryRangeSpec{Name: "3d", Lookback: 3 * 24 * time.Hour, Bucket: 2 * time.Hour}, nil
	case "7d":
		return trafficHistoryRangeSpec{Name: "7d", Lookback: 7 * 24 * time.Hour, Bucket: 6 * time.Hour}, nil
	case "30d":
		return trafficHistoryRangeSpec{Name: "30d", Lookback: 30 * 24 * time.Hour, Bucket: 24 * time.Hour}, nil
	default:
		return trafficHistoryRangeSpec{}, errors.New("unsupported device traffic history range")
	}
}

func parseDeviceTrafficHistoryCustomRange(fromStr string, toStr string) (trafficHistoryRangeSpec, time.Time, time.Time, error) {
	from, err := parseTrafficHistoryDateInput(fromStr)
	if err != nil {
		return trafficHistoryRangeSpec{}, time.Time{}, time.Time{}, errors.New("invalid 'from' date, expected YYYY-MM-DD")
	}

	to, err := parseTrafficHistoryDateInput(toStr)
	if err != nil {
		return trafficHistoryRangeSpec{}, time.Time{}, time.Time{}, errors.New("invalid 'to' date, expected YYYY-MM-DD")
	}
	if len(strings.TrimSpace(toStr)) == 10 {
		to = to.Add(24*time.Hour - time.Second)
	}

	from = from.UTC()
	to = to.UTC()
	if !to.After(from) {
		return trafficHistoryRangeSpec{}, time.Time{}, time.Time{}, errors.New("'to' must be after 'from'")
	}

	duration := to.Sub(from)
	var bucket time.Duration
	switch {
	case duration <= time.Hour:
		bucket = time.Minute
	case duration <= 3*time.Hour:
		bucket = 5 * time.Minute
	case duration <= 24*time.Hour:
		bucket = 30 * time.Minute
	case duration <= 3*24*time.Hour:
		bucket = 2 * time.Hour
	case duration <= 7*24*time.Hour:
		bucket = 6 * time.Hour
	default:
		bucket = 24 * time.Hour
	}

	return trafficHistoryRangeSpec{
		Name:     "custom",
		Lookback: duration,
		Bucket:   bucket,
	}, from, to, nil
}

func parseTrafficHistoryDateInput(raw string) (time.Time, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}, errors.New("empty date")
	}

	parsed, err := time.Parse("2006-01-02", value)
	if err == nil {
		return parsed, nil
	}

	parsed, err = time.Parse(time.RFC3339, value)
	if err == nil {
		return parsed, nil
	}

	return time.Time{}, err
}

func aggregateDeviceTrafficHistory(samples []deviceTrafficHistorySample, spec trafficHistoryRangeSpec, from time.Time, to time.Time, sampleInterval time.Duration, sourceIP string) DeviceTrafficHistoryResponse {
	response := DeviceTrafficHistoryResponse{
		SourceIP:              sourceIP,
		Range:                 spec.Name,
		BucketSeconds:         int(spec.Bucket.Seconds()),
		SampleIntervalSeconds: int(sampleInterval.Seconds()),
		Points:                []DeviceTrafficHistoryPoint{},
	}

	if from.IsZero() || to.IsZero() || !to.After(from) {
		return response
	}

	bucketStart := from.Truncate(spec.Bucket)
	for at := bucketStart; !at.After(to); at = at.Add(spec.Bucket) {
		response.Points = append(response.Points, DeviceTrafficHistoryPoint{
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

	if len(samples) == 0 {
		return response
	}

	sort.Slice(samples, func(i, j int) bool {
		return samples[i].At.Before(samples[j].At)
	})

	response.AvailableSince = samples[0].At.Format(time.RFC3339)
	response.LatestSampleAt = samples[len(samples)-1].At.Format(time.RFC3339)

	for _, sample := range samples {
		if sample.At.Before(from) || sample.At.After(to) {
			continue
		}

		bucketAt := sample.At.Truncate(spec.Bucket)
		pointPosition, exists := pointIndex[bucketAt.Unix()]
		if !exists {
			continue
		}

		if response.DeviceName == "" && strings.TrimSpace(sample.DeviceName) != "" {
			response.DeviceName = strings.TrimSpace(sample.DeviceName)
		}
		if response.DeviceMAC == "" && strings.TrimSpace(sample.DeviceMAC) != "" {
			response.DeviceMAC = strings.TrimSpace(sample.DeviceMAC)
		}

		response.Points[pointPosition].Bytes += sample.Bytes
		response.Points[pointPosition].Packets += sample.Packets
		response.Points[pointPosition].TunneledBytes += sample.TunneledBytes
		response.Points[pointPosition].DirectBytes += sample.DirectBytes
		response.TotalBytes += sample.Bytes
		response.TotalPackets += sample.Packets
		response.TunneledBytes += sample.TunneledBytes
		response.DirectBytes += sample.DirectBytes
	}

	for _, point := range response.Points {
		if point.Bytes > response.PeakBytes {
			response.PeakBytes = point.Bytes
		}
	}

	if response.DeviceName == "" {
		response.DeviceName = strings.TrimSpace(samples[len(samples)-1].DeviceName)
	}
	if response.DeviceMAC == "" {
		response.DeviceMAC = strings.TrimSpace(samples[len(samples)-1].DeviceMAC)
	}

	return response
}

func deviceTrafficHistoryBucketAt(raw string) string {
	if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(raw)); err == nil {
		return parsed.UTC().Truncate(time.Minute).Format(time.RFC3339)
	}
	return time.Now().UTC().Truncate(time.Minute).Format(time.RFC3339)
}

func (s *siteTrafficStore) ListDeviceHistory(sourceIP string, from time.Time, to time.Time) ([]deviceTrafficHistorySample, error) {
	if err := s.ensureReady(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`
		SELECT bucket_at, source_ip, device_name, device_mac, bytes, packets, tunneled_bytes, direct_bytes
		FROM device_traffic_history
		WHERE source_ip = ? AND bucket_at >= ? AND bucket_at <= ?
		ORDER BY bucket_at ASC
	`, strings.TrimSpace(sourceIP), from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	samples := make([]deviceTrafficHistorySample, 0, 128)
	for rows.Next() {
		var (
			bucketAtRaw string
			item        deviceTrafficHistorySample
		)
		if err := rows.Scan(
			&bucketAtRaw,
			&item.SourceIP,
			&item.DeviceName,
			&item.DeviceMAC,
			&item.Bytes,
			&item.Packets,
			&item.TunneledBytes,
			&item.DirectBytes,
		); err != nil {
			return nil, err
		}

		at, err := time.Parse(time.RFC3339, strings.TrimSpace(bucketAtRaw))
		if err != nil {
			continue
		}
		item.At = at.UTC()
		samples = append(samples, item)
	}

	return samples, rows.Err()
}
