package status

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

func (s *Service) SiteTrafficHistory(scope string, sortBy string, sourceIP string, search string, page int, pageSize int, rangeName string) (SiteTrafficResponse, error) {
	spec, err := parseDeviceTrafficHistoryRange(rangeName)
	if err != nil {
		return SiteTrafficResponse{}, err
	}

	now := time.Now().UTC()
	return s.siteTrafficHistoryWindow(scope, sortBy, sourceIP, search, page, pageSize, now.Add(-spec.Lookback), now)
}

func (s *Service) SiteTrafficHistoryCustom(scope string, sortBy string, sourceIP string, search string, page int, pageSize int, fromStr string, toStr string) (SiteTrafficResponse, error) {
	_, from, to, err := parseDeviceTrafficHistoryCustomRange(fromStr, toStr)
	if err != nil {
		return SiteTrafficResponse{}, err
	}

	return s.siteTrafficHistoryWindow(scope, sortBy, sourceIP, search, page, pageSize, from, to)
}

func (s *Service) siteTrafficHistoryWindow(scope string, sortBy string, sourceIP string, search string, page int, pageSize int, from time.Time, to time.Time) (SiteTrafficResponse, error) {
	page = normalizeTrafficPage(page)
	pageSize = normalizeTrafficPageSize(pageSize, defaultSiteTrafficPageSize)
	sourceIP = strings.TrimSpace(sourceIP)

	if sourceIP == "" {
		return SiteTrafficResponse{}, errors.New("sourceIp is required")
	}

	if s.siteTraffic == nil {
		return SiteTrafficResponse{
			Sites:      []SiteTrafficStat{},
			Page:       page,
			PageSize:   pageSize,
			Total:      0,
			TotalPages: 0,
			SourceIP:   sourceIP,
		}, nil
	}

	result, err := s.siteTraffic.ListHistory(scope, sortBy, sourceIP, search, page, pageSize, from, to)
	if err != nil {
		return SiteTrafficResponse{}, err
	}

	return SiteTrafficResponse{
		Sites:      result.Stats,
		TotalBytes: result.TotalBytes,
		UpdatedAt:  result.UpdatedAt,
		Page:       page,
		PageSize:   pageSize,
		Total:      result.TotalCount,
		TotalPages: totalTrafficPages(result.TotalCount, pageSize),
		SourceIP:   sourceIP,
	}, nil
}

func (s *siteTrafficStore) ListHistory(scope string, sortBy string, sourceIP string, search string, page int, pageSize int, from time.Time, to time.Time) (pagedSiteTrafficResult, error) {
	if err := s.ensureReady(); err != nil {
		return pagedSiteTrafficResult{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	conditions := []string{
		"source_ip = ?",
		"bucket_at >= ?",
		"bucket_at <= ?",
	}
	args := []any{
		strings.TrimSpace(sourceIP),
		from.UTC().Format(time.RFC3339),
		to.UTC().Format(time.RFC3339),
	}

	switch strings.TrimSpace(scope) {
	case "tunneled":
		conditions = append(conditions, "via_tunnel = 1")
	case "direct":
		conditions = append(conditions, "via_tunnel = 0")
	}

	if query := strings.ToLower(strings.TrimSpace(search)); query != "" {
		like := "%" + query + "%"
		conditions = append(conditions, "(LOWER(domain) LIKE ? OR last_ip LIKE ?)")
		args = append(args, like, like)
	}

	where := " WHERE " + strings.Join(conditions, " AND ")

	var totalCount int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM (SELECT domain FROM device_site_traffic_history`+where+` GROUP BY domain)`,
		args...,
	).Scan(&totalCount); err != nil {
		return pagedSiteTrafficResult{}, err
	}

	var totalBytes sql.NullInt64
	var updatedAt sql.NullString
	if err := s.db.QueryRow(
		`SELECT COALESCE(SUM(bytes), 0), COALESCE(MAX(bucket_at), '') FROM device_site_traffic_history`+where,
		args...,
	).Scan(&totalBytes, &updatedAt); err != nil {
		return pagedSiteTrafficResult{}, err
	}

	query := `
		SELECT
			domain,
			COALESCE(SUM(bytes), 0) AS bytes,
			COALESCE(SUM(packets), 0) AS packets,
			COALESCE(MAX(bucket_at), '') AS updated_at,
			COALESCE(MAX(last_ip), '') AS last_ip,
			COALESCE(MAX(via_tunnel), 0) AS via_tunnel,
			COALESCE(MAX(route_label), '') AS route_label
		FROM device_site_traffic_history` + where + `
		GROUP BY domain
		ORDER BY ` + siteTrafficOrderClause(sortBy) + `
		LIMIT ? OFFSET ?`
	queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)

	rows, err := s.db.Query(query, queryArgs...)
	if err != nil {
		return pagedSiteTrafficResult{}, err
	}
	defer rows.Close()

	stats := make([]SiteTrafficStat, 0, 64)
	for rows.Next() {
		var item SiteTrafficStat
		var viaTunnel int
		if err := rows.Scan(&item.Domain, &item.Bytes, &item.Packets, &item.UpdatedAt, &item.LastIP, &viaTunnel, &item.RouteLabel); err != nil {
			return pagedSiteTrafficResult{}, err
		}
		item.ViaTunnel = viaTunnel == 1
		stats = append(stats, item)
	}
	if err := rows.Err(); err != nil {
		return pagedSiteTrafficResult{}, err
	}

	return pagedSiteTrafficResult{
		Stats:      stats,
		TotalCount: totalCount,
		TotalBytes: nullInt64ToUint64(totalBytes),
		UpdatedAt:  updatedAt.String,
	}, nil
}
