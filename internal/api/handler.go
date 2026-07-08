package api

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"xiomi-router-driver/internal/automation"
	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/domains"
	"xiomi-router-driver/internal/events"
	"xiomi-router-driver/internal/openvpn"
	"xiomi-router-driver/internal/probe"
	"xiomi-router-driver/internal/routing"
	"xiomi-router-driver/internal/status"
	"xiomi-router-driver/internal/subscription"
	"xiomi-router-driver/internal/update"
)

type Dependencies struct {
	State                 *config.Manager
	Domains               *domains.Manager
	Events                *events.Store
	Routing               *routing.Runner
	Automation            *automation.Manager
	OpenVPN               *openvpn.Manager
	Subscriptions         *subscription.Manager
	Status                *status.Service
	Update                *update.Manager
	FailoverStatus        func() automation.FailoverStatus
	PriorityStatus        func() automation.PriorityStatus
	SetPriorityOverride   func(policyID string, location string) error
	ClearPriorityOverride func(policyID string)
	ApplyPriorityNow      func(ctx context.Context) error
	DataDir               string
}

type Handler struct {
	state                 *config.Manager
	domains               *domains.Manager
	events                *events.Store
	routing               *routing.Runner
	automation            *automation.Manager
	openvpn               *openvpn.Manager
	subscriptions         *subscription.Manager
	status                *status.Service
	update                *update.Manager
	failoverStatus        func() automation.FailoverStatus
	priorityStatus        func() automation.PriorityStatus
	setPriorityOverride   func(policyID string, location string) error
	clearPriorityOverride func(policyID string)
	applyPriorityNow      func(ctx context.Context) error
	dataDir               string
	router                http.Handler
	applyMu               sync.Mutex
}

type providerRequest struct {
	Name             string `json:"name"`
	Type             string `json:"type"`
	Source           string `json:"source"`
	SelectedLocation string `json:"selectedLocation"`
	Enabled          bool   `json:"enabled"`
}

type ruleRequest struct {
	Name             string `json:"name"`
	ProviderID       string `json:"providerId"`
	SelectedLocation string `json:"selectedLocation"`
	Domains          string `json:"domains"`
	Enabled          bool   `json:"enabled"`
}

type priorityPolicyRequest struct {
	Name       string                          `json:"name"`
	ProviderID string                          `json:"providerId"`
	Enabled    bool                            `json:"enabled"`
	Entries    []string                        `json:"entries"`
	Targets    []config.PriorityTarget         `json:"targets"`
	Schedule   []config.PriorityScheduleWindow `json:"schedule"`
}

type priorityOverrideRequest struct {
	Location string `json:"location"`
}

type applyResult struct {
	Status       string   `json:"status"`
	RulesApplied int      `json:"rulesApplied"`
	Domains      []string `json:"domains"`
}

const applyRequestTimeout = 2 * time.Minute

func NewHandler(deps Dependencies) *Handler {
	handler := &Handler{
		state:                 deps.State,
		domains:               deps.Domains,
		events:                deps.Events,
		routing:               deps.Routing,
		automation:            deps.Automation,
		openvpn:               deps.OpenVPN,
		subscriptions:         deps.Subscriptions,
		status:                deps.Status,
		update:                deps.Update,
		failoverStatus:        deps.FailoverStatus,
		priorityStatus:        deps.PriorityStatus,
		setPriorityOverride:   deps.SetPriorityOverride,
		clearPriorityOverride: deps.ClearPriorityOverride,
		applyPriorityNow:      deps.ApplyPriorityNow,
		dataDir:               deps.DataDir,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", handler.handleStatus)
	mux.HandleFunc("/api/failover/status", handler.handleFailoverStatus)
	mux.HandleFunc("/api/traffic/history", handler.handleTrafficHistory)
	mux.HandleFunc("/api/traffic/domains", handler.handleDomainTraffic)
	mux.HandleFunc("/api/traffic/sites/history", handler.handleSiteTrafficHistory)
	mux.HandleFunc("/api/traffic/sites", handler.handleSiteTraffic)
	mux.HandleFunc("/api/traffic/devices/history", handler.handleDeviceTrafficHistory)
	mux.HandleFunc("/api/traffic/devices", handler.handleDeviceTraffic)
	mux.HandleFunc("/api/config", handler.handleConfig)
	mux.HandleFunc("/api/config/routing", handler.handleRoutingConfig)
	mux.HandleFunc("/api/config/automation", handler.handleAutomationConfig)
	mux.HandleFunc("/api/priority-policies/status", handler.handlePriorityPolicyStatus)
	mux.HandleFunc("/api/priority-policies", handler.handlePriorityPolicies)
	mux.HandleFunc("/api/priority-policies/", handler.handlePriorityPolicy)
	mux.HandleFunc("/api/events", handler.handleEvents)
	mux.HandleFunc("/api/providers/probe", handler.handleProbeProvider)
	mux.HandleFunc("/api/providers/latency", handler.handleProviderLatency)
	mux.HandleFunc("/api/providers/upload", handler.handleUploadProfile)
	mux.HandleFunc("/api/providers", handler.handleProviders)
	mux.HandleFunc("/api/providers/", handler.handleProvider)
	mux.HandleFunc("/api/rules", handler.handleRules)
	mux.HandleFunc("/api/rules/", handler.handleRule)
	mux.HandleFunc("/api/rules/apply", handler.handleApplyRules)
	mux.HandleFunc("/api/domains/health", handler.handleDomainHealth)
	mux.HandleFunc("/api/domains/health/check", handler.handleCheckDomainHealth)
	mux.HandleFunc("/api/domains", handler.handleDomainsPreview)
	mux.HandleFunc("/api/system/resources", handler.handleSystemResources)
	mux.HandleFunc("/api/system/reboot", handler.handleReboot)
	mux.HandleFunc("/api/system/update", handler.handleSystemUpdate)
	mux.HandleFunc("/api/system/update/settings", handler.handleSystemUpdateSettings)
	mux.HandleFunc("/api/system/update/check", handler.handleSystemUpdateCheck)
	mux.HandleFunc("/api/system/update/install", handler.handleSystemUpdateInstall)
	mux.HandleFunc("/api/system/update/upload", handler.handleSystemUpdateUpload)
	handler.router = mux
	return handler
}

func (h *Handler) SetFailoverStatusProvider(provider func() automation.FailoverStatus) {
	h.failoverStatus = provider
}

func (h *Handler) SetPriorityRuntime(statusProvider func() automation.PriorityStatus, setOverride func(policyID string, location string) error, clearOverride func(policyID string), applyNow func(ctx context.Context) error) {
	h.priorityStatus = statusProvider
	h.setPriorityOverride = setOverride
	h.clearPriorityOverride = clearOverride
	h.applyPriorityNow = applyNow
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.router.ServeHTTP(w, r)
}

func (h *Handler) handleEvents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodDelete:
		if err := h.events.Clear(); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "cleared"})
		return
	case http.MethodGet:
		// handled below
	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodDelete)
		return
	}

	limit := 25
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("limit must be an integer"))
			return
		}
		limit = parsed
	}

	offset := 0
	if rawOffset := strings.TrimSpace(r.URL.Query().Get("offset")); rawOffset != "" {
		parsed, err := strconv.Atoi(rawOffset)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("offset must be an integer"))
			return
		}
		offset = parsed
	}

	list, total, err := h.events.List(limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"events": list,
		"count":  len(list),
		"total":  total,
	})
}

func (h *Handler) handleFailoverStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	if h.failoverStatus == nil {
		writeJSON(w, http.StatusOK, automation.FailoverStatus{})
		return
	}
	writeJSON(w, http.StatusOK, h.failoverStatus())
}

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	snapshot, err := h.status.Snapshot(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, snapshot)
}

func (h *Handler) handleSystemResources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	resources := status.CollectSystemResources(h.dataDir)
	writeJSON(w, http.StatusOK, resources)
}

func (h *Handler) handleReboot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}

	if runtime.GOOS != "linux" {
		writeError(w, http.StatusServiceUnavailable, errors.New("reboot is only supported on the router (Linux)"))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "rebooting"})

	go func() {
		time.Sleep(500 * time.Millisecond)
		if err := exec.Command("reboot").Run(); err != nil {
			log.Printf("reboot command failed: %v", err)
		}
	}()
}

func (h *Handler) handleSystemUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	if h.update == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("update manager is not configured"))
		return
	}

	status, err := h.update.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *Handler) handleSystemUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeMethodNotAllowed(w, http.MethodPut)
		return
	}
	if h.update == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("update manager is not configured"))
		return
	}

	var settings config.UpdateSettings
	if err := decodeJSON(r, &settings); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	status, err := h.update.SaveSettings(r.Context(), settings)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *Handler) handleSystemUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	if h.update == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("update manager is not configured"))
		return
	}

	status, err := h.update.Check(r.Context())
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *Handler) handleSystemUpdateInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	if h.update == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("update manager is not configured"))
		return
	}

	result, err := h.update.InstallLatest(r.Context())
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleSystemUpdateUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	if h.update == nil {
		writeError(w, http.StatusServiceUnavailable, errors.New("update manager is not configured"))
		return
	}

	if err := r.ParseMultipartForm(128 << 20); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	file, header, err := r.FormFile("archive")
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("archive file is required"))
		return
	}
	defer file.Close()

	result, err := h.update.InstallUploaded(r.Context(), file, header.Filename)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleTrafficHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	q := r.URL.Query()
	fromStr := q.Get("from")
	toStr := q.Get("to")

	var history status.TrafficHistoryResponse
	var err error
	if fromStr != "" && toStr != "" {
		history, err = h.status.TrafficHistoryCustom(fromStr, toStr)
	} else {
		history, err = h.status.TrafficHistory(q.Get("range"))
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, http.StatusOK, history)
}

func (h *Handler) handleDomainTraffic(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		sortBy := q.Get("sort")
		limit := 0
		if v := q.Get("limit"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
				limit = parsed
			}
		}

		var (
			result status.DomainTrafficResponse
			err    error
		)
		if truthyQueryValue(q.Get("live")) {
			result, err = h.status.LiveDomainTraffic(sortBy, limit)
		} else {
			result, err = h.status.DomainTraffic(sortBy, limit)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusOK, result)

	case http.MethodPost:
		q := r.URL.Query()
		sortBy := q.Get("sort")
		limit := 0
		if v := q.Get("limit"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
				limit = parsed
			}
		}

		if err := h.status.SampleDomainTraffic(); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		result, err := h.status.DomainTraffic(sortBy, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusOK, result)

	case http.MethodDelete:
		if err := h.status.ResetDomainTraffic(); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})

	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPost, http.MethodDelete)
	}
}

func (h *Handler) handleSiteTraffic(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		sortBy := q.Get("sort")
		order := q.Get("order")
		scope := q.Get("scope")
		search := q.Get("query")
		sourceIP := q.Get("sourceIp")
		pageSize := parsePositiveQueryIntWithLegacy(q, "pageSize", "limit", 20)
		page := parsePositiveQueryInt(q, "page", 1)

		result, err := h.status.SiteTraffic(scope, sortBy, order, sourceIP, search, page, pageSize)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusOK, result)

	case http.MethodPost:
		q := r.URL.Query()
		sortBy := q.Get("sort")
		order := q.Get("order")
		scope := q.Get("scope")
		search := q.Get("query")
		sourceIP := q.Get("sourceIp")
		pageSize := parsePositiveQueryIntWithLegacy(q, "pageSize", "limit", 20)
		page := parsePositiveQueryInt(q, "page", 1)

		if err := h.status.SampleSiteTraffic(); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		result, err := h.status.SiteTraffic(scope, sortBy, order, sourceIP, search, page, pageSize)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusOK, result)

	case http.MethodDelete:
		if err := h.status.ResetSiteTraffic(); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})

	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPost, http.MethodDelete)
	}
}

func (h *Handler) handleSiteTrafficHistory(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet, http.MethodPost:
		q := r.URL.Query()
		sortBy := q.Get("sort")
		order := q.Get("order")
		scope := q.Get("scope")
		search := q.Get("query")
		sourceIP := strings.TrimSpace(q.Get("sourceIp"))
		pageSize := parsePositiveQueryIntWithLegacy(q, "pageSize", "limit", 20)
		page := parsePositiveQueryInt(q, "page", 1)

		if sourceIP == "" {
			writeError(w, http.StatusBadRequest, errors.New("sourceIp is required"))
			return
		}

		if r.Method == http.MethodPost {
			if err := h.status.SampleSiteTraffic(); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}

		fromStr := q.Get("from")
		toStr := q.Get("to")

		var (
			result status.SiteTrafficResponse
			err    error
		)
		if fromStr != "" && toStr != "" {
			result, err = h.status.SiteTrafficHistoryCustom(scope, sortBy, order, sourceIP, search, page, pageSize, fromStr, toStr)
		} else {
			result, err = h.status.SiteTrafficHistory(scope, sortBy, order, sourceIP, search, page, pageSize, q.Get("range"))
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		writeJSON(w, http.StatusOK, result)

	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (h *Handler) handleDeviceTraffic(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		sortBy := q.Get("sort")
		order := q.Get("order")
		scope := q.Get("scope")
		search := q.Get("query")
		sourceIP := q.Get("sourceIp")
		pageSize := parsePositiveQueryIntWithLegacy(q, "pageSize", "limit", 6)
		page := parsePositiveQueryInt(q, "page", 1)
		siteLimit := 5
		if v := q.Get("siteLimit"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 {
				siteLimit = parsed
			}
		}

		result, err := h.status.DeviceTraffic(scope, sortBy, order, sourceIP, search, page, pageSize, siteLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusOK, result)

	case http.MethodPost:
		q := r.URL.Query()
		sortBy := q.Get("sort")
		order := q.Get("order")
		scope := q.Get("scope")
		search := q.Get("query")
		sourceIP := q.Get("sourceIp")
		pageSize := parsePositiveQueryIntWithLegacy(q, "pageSize", "limit", 6)
		page := parsePositiveQueryInt(q, "page", 1)
		siteLimit := 5
		if v := q.Get("siteLimit"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 {
				siteLimit = parsed
			}
		}

		if err := h.status.SampleSiteTraffic(); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		result, err := h.status.DeviceTraffic(scope, sortBy, order, sourceIP, search, page, pageSize, siteLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusOK, result)

	case http.MethodDelete:
		if err := h.status.ResetSiteTraffic(); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})

	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPost, http.MethodDelete)
	}
}

func (h *Handler) handleDeviceTrafficHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	q := r.URL.Query()
	sourceIP := strings.TrimSpace(q.Get("sourceIp"))
	if sourceIP == "" {
		writeError(w, http.StatusBadRequest, errors.New("sourceIp is required"))
		return
	}

	fromStr := q.Get("from")
	toStr := q.Get("to")

	var (
		history status.DeviceTrafficHistoryResponse
		err     error
	)
	if fromStr != "" && toStr != "" {
		history, err = h.status.DeviceTrafficHistoryCustom(sourceIP, fromStr, toStr)
	} else {
		history, err = h.status.DeviceTrafficHistory(sourceIP, q.Get("range"))
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, http.StatusOK, history)
}

func (h *Handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	state, err := h.state.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, state)
}

func (h *Handler) handleRoutingConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state, err := h.state.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"routing": state.Routing})
	case http.MethodPut:
		var settings config.RoutingSettings
		if err := decodeJSON(r, &settings); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		settings, err := validateRoutingSettings(settings)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		state, err := h.state.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		state.Routing = settings
		saved, err := h.state.Save(state)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusOK, saved)
		h.recordEvent("info", "routing.updated", "Routing settings updated")
	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}

func (h *Handler) handleAutomationConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state, err := h.state.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"automation": state.Automation})
	case http.MethodPut:
		var settings config.AutomationSettings
		if err := decodeJSON(r, &settings); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		if h.automation != nil {
			if err := h.automation.Validate(settings); err != nil {
				writeError(w, http.StatusConflict, err)
				return
			}
		} else if settings.InstallService {
			writeError(w, http.StatusConflict, errors.New("system service manager is not configured"))
			return
		}

		state, err := h.state.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		previous := state
		state.Automation = settings

		saved, err := h.state.Save(state)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		if h.automation != nil {
			if err := h.automation.Sync(saved.Automation); err != nil {
				_, _ = h.state.Save(previous)
				_ = h.automation.Sync(previous.Automation)
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}

		writeJSON(w, http.StatusOK, saved)
		h.recordEvent("info", "automation.updated", fmt.Sprintf("Automation updated: service=%t, recover=%t", saved.Automation.InstallService, saved.Automation.AutoRecover))
		if saved.Automation.InstallService {
			h.recordEvent("info", "service.installed", fmt.Sprintf("System service installed at %s", h.automation.ServicePath()))
		} else {
			h.recordEvent("warn", "service.disabled", "System service autostart disabled")
		}
	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPut)
	}
}

func (h *Handler) handlePriorityPolicyStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	if h.priorityStatus == nil {
		writeJSON(w, http.StatusOK, automation.PriorityStatus{})
		return
	}
	writeJSON(w, http.StatusOK, h.priorityStatus())
}

func (h *Handler) handlePriorityPolicies(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state, err := h.state.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		providerID := strings.TrimSpace(r.URL.Query().Get("providerId"))
		policies := state.PriorityPolicies
		if providerID != "" {
			filtered := make([]config.PriorityPolicy, 0, len(policies))
			for _, policy := range policies {
				if policy.ProviderID == providerID {
					filtered = append(filtered, policy)
				}
			}
			policies = filtered
		}
		writeJSON(w, http.StatusOK, map[string]any{"policies": policies})
	case http.MethodPost:
		var req priorityPolicyRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		state, err := h.state.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		policy, err := h.buildPriorityPolicy("", req, state)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		nextState := state
		nextState.PriorityPolicies = append(append([]config.PriorityPolicy(nil), state.PriorityPolicies...), policy)
		if err := validatePriorityPolicyState(nextState); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := validateActiveRuleEntries(nextState); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		saved, err := h.state.Save(nextState)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"policy": policy, "count": len(saved.PriorityPolicies)})
		h.recordEvent("info", "priority_policy.created", fmt.Sprintf("Priority policy %q created", policy.Name))
	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (h *Handler) handlePriorityPolicy(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/priority-policies/")
	path = strings.Trim(path, "/")
	if path == "" {
		writeError(w, http.StatusBadRequest, errors.New("priority policy id is required"))
		return
	}
	parts := strings.Split(path, "/")
	id, err := url.PathUnescape(parts[0])
	if err != nil || strings.TrimSpace(id) == "" {
		writeError(w, http.StatusBadRequest, errors.New("priority policy id is invalid"))
		return
	}
	if len(parts) > 1 {
		if len(parts) == 2 && parts[1] == "override" {
			h.handlePriorityPolicyOverride(w, r, id)
			return
		}
		writeError(w, http.StatusNotFound, fmt.Errorf("priority policy path %q not found", r.URL.Path))
		return
	}

	switch r.Method {
	case http.MethodPut:
		var req priorityPolicyRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		state, err := h.state.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		index := findPriorityPolicyIndex(state.PriorityPolicies, id)
		if index < 0 {
			writeError(w, http.StatusNotFound, fmt.Errorf("priority policy %s not found", id))
			return
		}
		policy, err := h.buildPriorityPolicy(id, req, state)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		nextState := state
		nextState.PriorityPolicies = append([]config.PriorityPolicy(nil), state.PriorityPolicies...)
		nextState.PriorityPolicies[index] = policy
		if err := validatePriorityPolicyState(nextState); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := validateActiveRuleEntries(nextState); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		saved, err := h.state.Save(nextState)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"policy": saved.PriorityPolicies[index]})
		h.recordEvent("info", "priority_policy.updated", fmt.Sprintf("Priority policy %q updated", policy.Name))
	case http.MethodDelete:
		state, err := h.state.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		index := findPriorityPolicyIndex(state.PriorityPolicies, id)
		if index < 0 {
			writeError(w, http.StatusNotFound, fmt.Errorf("priority policy %s not found", id))
			return
		}
		nextState := state
		nextState.PriorityPolicies = append([]config.PriorityPolicy(nil), state.PriorityPolicies[:index]...)
		nextState.PriorityPolicies = append(nextState.PriorityPolicies, state.PriorityPolicies[index+1:]...)
		if _, err := h.state.Save(nextState); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if h.clearPriorityOverride != nil {
			h.clearPriorityOverride(id)
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		h.recordEvent("warn", "priority_policy.deleted", fmt.Sprintf("Priority policy %s deleted", id))
	default:
		writeMethodNotAllowed(w, http.MethodPut, http.MethodDelete)
	}
}

func (h *Handler) handlePriorityPolicyOverride(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodPost:
		var req priorityOverrideRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		state, err := h.state.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		policy, ok := findPriorityPolicy(state.PriorityPolicies, id)
		if !ok {
			writeError(w, http.StatusNotFound, fmt.Errorf("priority policy %s not found", id))
			return
		}
		location := strings.TrimSpace(req.Location)
		if !priorityPolicyHasTarget(policy, location) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("location %q is not in priority policy targets", location))
			return
		}
		if h.setPriorityOverride == nil {
			writeError(w, http.StatusConflict, errors.New("priority runtime is not configured"))
			return
		}
		if err := h.setPriorityOverride(id, location); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if h.applyPriorityNow != nil {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), applyRequestTimeout)
			defer cancel()
			if err := h.applyPriorityNow(ctx); err != nil {
				writeApplyError(w, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "override_set"})
	case http.MethodDelete:
		if h.clearPriorityOverride != nil {
			h.clearPriorityOverride(id)
		}
		if h.applyPriorityNow != nil {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), applyRequestTimeout)
			defer cancel()
			if err := h.applyPriorityNow(ctx); err != nil {
				writeApplyError(w, err)
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "override_cleared"})
	default:
		writeMethodNotAllowed(w, http.MethodPost, http.MethodDelete)
	}
}

func (h *Handler) handleProviders(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state, err := h.state.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"providers": state.Providers})
	case http.MethodPost:
		var req providerRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		provider, err := buildProvider("", req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		state, err := h.state.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		state.Providers = append(state.Providers, provider)
		saved, err := h.state.Save(state)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"provider": provider,
			"count":    len(saved.Providers),
		})
		h.recordEvent("info", "provider.created", fmt.Sprintf("Provider %q created", provider.Name))
	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (h *Handler) handleUploadProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}

	if err := r.ParseMultipartForm(2 << 20); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("parse multipart form: %w", err))
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("file is required: %w", err))
		return
	}
	defer file.Close()

	if !strings.HasSuffix(strings.ToLower(header.Filename), ".ovpn") {
		writeError(w, http.StatusBadRequest, errors.New("only .ovpn files are allowed"))
		return
	}

	profilesDir := filepath.Join(h.dataDir, "profiles")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("create profiles dir: %w", err))
		return
	}

	safeName := filepath.Base(header.Filename)
	destPath := filepath.Join(profilesDir, safeName)

	dest, err := os.Create(destPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("create profile destination: %w", err))
		return
	}
	defer dest.Close()

	if _, err := io.Copy(dest, file); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("save profile: %w", err))
		return
	}

	relativePath := "profiles/" + safeName
	writeJSON(w, http.StatusOK, map[string]string{
		"path":     relativePath,
		"filename": safeName,
	})
	h.recordEvent("info", "profile.uploaded", fmt.Sprintf("Profile %s uploaded", safeName))
}

func (h *Handler) handleProbeProvider(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}

	var req struct {
		Type   string `json:"type"`
		Source string `json:"source"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, http.StatusOK, probe.ProbeSource(req.Type, req.Source, h.dataDir))
}

func (h *Handler) handleProviderLatency(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}

	var req struct {
		Locations []probe.Location `json:"locations"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"locations": probe.MeasureLatencies(r.Context(), req.Locations),
	})
}

func (h *Handler) handleProvider(w http.ResponseWriter, r *http.Request) {
	id, action, err := extractProviderAction(r.URL.Path)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if action != "" {
		switch action {
		case "refresh":
			h.handleProviderRefresh(w, r, id)
		default:
			writeError(w, http.StatusNotFound, fmt.Errorf("provider action %q not found", action))
		}
		return
	}

	switch r.Method {
	case http.MethodPut:
		var req providerRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		provider, err := buildProvider(id, req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		state, err := h.state.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		index := findProviderIndex(state.Providers, id)
		if index < 0 {
			writeError(w, http.StatusNotFound, fmt.Errorf("provider %s not found", id))
			return
		}
		previous := state.Providers[index]
		if !previous.Enabled && provider.Enabled {
			nextState := state
			nextState.Providers = append([]config.Provider(nil), state.Providers...)
			nextState.Providers[index] = provider
			if err := validateActiveRuleEntries(nextState); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}

		state.Providers[index] = provider
		if _, err := h.state.Save(state); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"provider": provider})
		h.recordEvent("info", "provider.updated", fmt.Sprintf("Provider %q updated", provider.Name))
	case http.MethodDelete:
		state, err := h.state.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		nextProviders := make([]config.Provider, 0, len(state.Providers))
		found := false
		for _, provider := range state.Providers {
			if provider.ID == id {
				found = true
				continue
			}
			nextProviders = append(nextProviders, provider)
		}
		if !found {
			writeError(w, http.StatusNotFound, fmt.Errorf("provider %s not found", id))
			return
		}

		nextRules := make([]config.Rule, 0, len(state.Rules))
		for _, rule := range state.Rules {
			if rule.ProviderID == id {
				continue
			}
			nextRules = append(nextRules, rule)
		}
		nextPolicies := make([]config.PriorityPolicy, 0, len(state.PriorityPolicies))
		for _, policy := range state.PriorityPolicies {
			if policy.ProviderID == id {
				continue
			}
			nextPolicies = append(nextPolicies, policy)
		}

		state.Providers = nextProviders
		state.Rules = nextRules
		state.PriorityPolicies = nextPolicies
		if _, err := h.state.Save(state); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		h.recordEvent("warn", "provider.deleted", fmt.Sprintf("Provider %s deleted", id))
	default:
		writeMethodNotAllowed(w, http.MethodPut, http.MethodDelete)
	}
}

func (h *Handler) handleProviderRefresh(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}

	state, err := h.state.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	index := findProviderIndex(state.Providers, id)
	if index < 0 {
		writeError(w, http.StatusNotFound, fmt.Errorf("provider %s not found", id))
		return
	}

	provider := state.Providers[index]
	if provider.Type != config.ProviderTypeSubscription {
		writeError(w, http.StatusBadRequest, errors.New("provider is not a subscription"))
		return
	}

	entries, err := subscription.RefreshEntriesCached(provider.Source, h.subscriptionRuntimeDir())
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("refresh subscription %q: %w", provider.Name, err))
		return
	}

	payload := map[string]any{
		"status":  "refreshed",
		"entries": len(entries),
		"applied": false,
	}
	if provider.Enabled && providerHasEnabledRules(state, provider.ID) {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), applyRequestTimeout)
		defer cancel()

		result, err := h.applyCurrentRules(ctx)
		if err != nil {
			writeApplyError(w, err)
			return
		}
		payload["applied"] = true
		payload["apply"] = result
	}

	writeJSON(w, http.StatusOK, payload)
	h.recordEvent("info", "subscription.refreshed", fmt.Sprintf("Subscription %q refreshed (%d entries)", provider.Name, len(entries)))
}

func (h *Handler) handleRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		state, err := h.state.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rules": state.Rules})
	case http.MethodPost:
		var req ruleRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		state, err := h.state.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		rule, err := buildRule("", req, state.Providers)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		state.Rules = append(state.Rules, rule)
		if err := validateActiveRuleEntries(state); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		saved, err := h.state.Save(state)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusCreated, map[string]any{
			"rule":  rule,
			"count": len(saved.Rules),
		})
		h.recordEvent("info", "rule.created", fmt.Sprintf("Rule %q created", rule.Name))
	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (h *Handler) handleRule(w http.ResponseWriter, r *http.Request) {
	id, err := extractID(r.URL.Path, "/api/rules/")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	switch r.Method {
	case http.MethodPut:
		var req ruleRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		state, err := h.state.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		rule, err := buildRule(id, req, state.Providers)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		index := findRuleIndex(state.Rules, id)
		if index < 0 {
			writeError(w, http.StatusNotFound, fmt.Errorf("rule %s not found", id))
			return
		}
		nextState := state
		nextState.Rules = append([]config.Rule(nil), state.Rules...)
		nextState.Rules[index] = rule
		if err := validateActiveRuleEntries(nextState); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		savedRule, err := h.state.UpdateRule(rule)
		if err != nil {
			switch {
			case errors.Is(err, sql.ErrNoRows):
				writeError(w, http.StatusNotFound, fmt.Errorf("rule %s not found", id))
			default:
				writeError(w, http.StatusInternalServerError, err)
			}
			return
		}

		if requestWantsApply(r) {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), applyRequestTimeout)
			defer cancel()

			result, err := h.applyCurrentRulesWithRollback(ctx, state)
			if err != nil {
				writeApplyError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"rule": savedRule, "apply": result})
			h.recordEvent("info", "rule.updated", fmt.Sprintf("Rule %q updated and applied", savedRule.Name))
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"rule": savedRule})
		h.recordEvent("info", "rule.updated", fmt.Sprintf("Rule %q updated", savedRule.Name))
	case http.MethodDelete:
		state, err := h.state.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if findRuleIndex(state.Rules, id) < 0 {
			writeError(w, http.StatusNotFound, fmt.Errorf("rule %s not found", id))
			return
		}

		if err := h.state.DeleteRule(id); err != nil {
			switch {
			case errors.Is(err, sql.ErrNoRows):
				writeError(w, http.StatusNotFound, fmt.Errorf("rule %s not found", id))
			default:
				writeError(w, http.StatusInternalServerError, err)
			}
			return
		}

		if requestWantsApply(r) {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), applyRequestTimeout)
			defer cancel()

			result, err := h.applyCurrentRulesWithRollback(ctx, state)
			if err != nil {
				writeApplyError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "apply": result})
			h.recordEvent("warn", "rule.deleted", fmt.Sprintf("Rule %s deleted and applied", id))
			return
		}

		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		h.recordEvent("warn", "rule.deleted", fmt.Sprintf("Rule %s deleted", id))
	default:
		writeMethodNotAllowed(w, http.MethodPut, http.MethodDelete)
	}
}

func (h *Handler) handleApplyRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), applyRequestTimeout)
	defer cancel()

	result, err := h.applyCurrentRules(ctx)
	if err != nil {
		writeApplyError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func writeApplyError(w http.ResponseWriter, err error) {
	statusCode := http.StatusInternalServerError
	switch {
	case strings.Contains(err.Error(), "cannot be used"):
		statusCode = http.StatusBadRequest
	case strings.Contains(err.Error(), "not implemented yet"):
		statusCode = http.StatusConflict
	case strings.Contains(err.Error(), "is not configured"):
		statusCode = http.StatusInternalServerError
	}
	writeError(w, statusCode, err)
}

func requestWantsApply(r *http.Request) bool {
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("apply"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (h *Handler) ApplyCurrentRules(ctx context.Context) error {
	_, err := h.applyCurrentRules(ctx)
	return err
}

func (h *Handler) applyCurrentRulesWithRollback(ctx context.Context, previousState config.State) (applyResult, error) {
	result, err := h.applyCurrentRules(ctx)
	if err == nil {
		return result, nil
	}

	if rollbackErr := h.rollbackFailedRuleApply(previousState); rollbackErr != nil {
		h.recordEvent("error", "rules.apply_rollback_failed", fmt.Sprintf("%v; rollback failed: %v", err, rollbackErr))
		return applyResult{}, fmt.Errorf("%w; rollback failed: %v", err, rollbackErr)
	}

	h.recordEvent("warn", "rules.apply_rolled_back", fmt.Sprintf("Rolled back configuration after apply failure: %v", err))
	return applyResult{}, err
}

func (h *Handler) rollbackFailedRuleApply(previousState config.State) error {
	if _, err := h.state.Save(previousState); err != nil {
		return fmt.Errorf("restore previous config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), applyRequestTimeout)
	defer cancel()

	if _, err := h.applyCurrentRules(ctx); err != nil {
		return fmt.Errorf("restore previous runtime: %w", err)
	}

	return nil
}

func (h *Handler) applyCurrentRules(ctx context.Context) (applyResult, error) {
	state, err := h.state.Load()
	if err != nil {
		return applyResult{}, err
	}
	return h.applyStateRules(ctx, state, true)
}

func (h *Handler) ApplyRulesFromState(ctx context.Context, state config.State) error {
	_, err := h.applyStateRules(ctx, state, false)
	return err
}

func (h *Handler) applyStateRules(ctx context.Context, state config.State, persistState bool) (applyResult, error) {
	h.applyMu.Lock()
	defer h.applyMu.Unlock()
	savedState := state
	state = automation.ApplyPriorityDefaults(state, time.Now())
	var previousDomains []string
	domainReplaceAttempted := false

	fail := func(err error) (applyResult, error) {
		finalErr := err
		if domainReplaceAttempted && h.domains != nil {
			if restoreErr := h.domains.ReplaceAll(previousDomains); restoreErr != nil {
				finalErr = fmt.Errorf("%w; restore domains failed: %v", err, restoreErr)
			}
		}
		if persistState {
			savedState.LastError = finalErr.Error()
			_, _ = h.state.Save(savedState)
		}
		h.recordEvent("error", "rules.apply_failed", finalErr.Error())
		return applyResult{}, finalErr
	}

	if err := validateActiveRuleEntries(state); err != nil {
		return fail(err)
	}

	providersByID := make(map[string]config.Provider, len(state.Providers))
	for _, provider := range state.Providers {
		providersByID[provider.ID] = provider
	}

	enabledRules := make([]config.Rule, 0, len(state.Rules))
	subscriptionRules := make([]config.Rule, 0, len(state.Rules))
	openvpnRules := make([]config.Rule, 0, len(state.Rules))
	activeProviders := make(map[string]config.Provider)
	domainsToApply := make([]string, 0, 32)
	openvpnDomains := make([]string, 0, 16)
	seenDomains := make(map[string]struct{})
	openvpnSeenDomains := make(map[string]struct{})

	for _, rule := range state.Rules {
		if !rule.Enabled {
			continue
		}

		provider, exists := providersByID[rule.ProviderID]
		if !exists || !provider.Enabled {
			continue
		}

		enabledRules = append(enabledRules, rule)
		activeProviders[rule.ProviderID] = provider
		switch provider.Type {
		case config.ProviderTypeSubscription:
			subscriptionRules = append(subscriptionRules, rule)
		case config.ProviderTypeOpenVPN:
			openvpnRules = append(openvpnRules, rule)
		}
		for _, domain := range rule.Domains {
			if _, exists := seenDomains[domain]; exists {
				continue
			}
			seenDomains[domain] = struct{}{}
			domainsToApply = append(domainsToApply, domain)
			if provider.Type == config.ProviderTypeOpenVPN {
				if _, exists := openvpnSeenDomains[domain]; exists {
					continue
				}
				openvpnSeenDomains[domain] = struct{}{}
				openvpnDomains = append(openvpnDomains, domain)
			}
		}
	}

	if h.domains == nil {
		return fail(errors.New("domains manager is not configured"))
	}
	var err error
	previousDomains, err = h.domains.List()
	if err != nil {
		return fail(err)
	}
	domainReplaceAttempted = true
	if err := h.domains.ReplaceAll(domainsToApply); err != nil {
		return fail(err)
	}

	if len(enabledRules) == 0 {
		var cleanupErrors []error
		if h.subscriptions != nil {
			if err := h.subscriptions.Cleanup(ctx); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
		if h.openvpn != nil {
			if err := h.openvpn.Cleanup(ctx); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		} else {
			if err := h.routing.Run(ctx, "del", state.Routing); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
		if len(cleanupErrors) > 0 {
			return fail(errors.Join(cleanupErrors...))
		}
	} else {
		var openvpnProvider config.Provider
		openvpnProviderCount := 0
		for _, provider := range activeProviders {
			if provider.Type == config.ProviderTypeOpenVPN {
				openvpnProvider = provider
				openvpnProviderCount++
			}
		}
		if openvpnProviderCount > 1 {
			return fail(errors.New("simultaneous apply for multiple openvpn providers is not implemented yet"))
		}

		if len(openvpnRules) > 0 {
			if h.openvpn == nil {
				return fail(errors.New("openvpn runtime manager is not configured"))
			}
			if err := h.openvpn.Apply(ctx, openvpnProvider, openvpnDomains, state.Routing); err != nil {
				return fail(err)
			}
		} else if h.openvpn != nil {
			if err := h.openvpn.Cleanup(ctx); err != nil {
				return fail(err)
			}
		} else {
			if err := h.routing.Run(ctx, "del", state.Routing); err != nil {
				return fail(err)
			}
		}

		if len(subscriptionRules) > 0 {
			if h.subscriptions == nil {
				if h.openvpn != nil {
					_ = h.openvpn.Cleanup(ctx)
				}
				return fail(errors.New("subscription runtime manager is not configured"))
			}
			subscriptionState := state
			if len(openvpnRules) > 0 {
				subscriptionState.Routing = shiftRoutingSettings(subscriptionState.Routing, 1)
			}
			if err := h.subscriptions.Apply(ctx, subscriptionState, subscriptionRules); err != nil {
				if h.openvpn != nil {
					_ = h.openvpn.Cleanup(ctx)
				}
				return fail(err)
			}
		} else if h.subscriptions != nil {
			if err := h.subscriptions.Cleanup(ctx); err != nil {
				if h.openvpn != nil {
					_ = h.openvpn.Cleanup(ctx)
				}
				return fail(err)
			}
		}
	}

	if persistState {
		savedState.LastAppliedAt = time.Now().UTC().Format(time.RFC3339)
		savedState.LastError = ""
		_, _ = h.state.Save(savedState)
	}

	eventMessage := fmt.Sprintf("Applied %d rules for %d routing entries", len(enabledRules), len(domainsToApply))
	if !persistState {
		eventMessage = fmt.Sprintf("Applied failover runtime state: %d rules for %d routing entries", len(enabledRules), len(domainsToApply))
	}
	h.recordEvent("info", "rules.applied", eventMessage)
	return applyResult{
		Status:       "applied",
		RulesApplied: len(enabledRules),
		Domains:      domainsToApply,
	}, nil
}

func (h *Handler) handleDomainsPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	list, err := h.domains.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"domains": list,
		"count":   len(list),
	})
}

func (h *Handler) handleDomainHealth(w http.ResponseWriter, r *http.Request) {
	if h.status == nil {
		writeError(w, http.StatusNotImplemented, errors.New("status service is not configured"))
		return
	}
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}

	response, err := h.status.DomainHealth()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) handleCheckDomainHealth(w http.ResponseWriter, r *http.Request) {
	if h.status == nil {
		writeError(w, http.StatusNotImplemented, errors.New("status service is not configured"))
		return
	}
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}

	var request struct {
		Domains []string `json:"domains"`
	}

	if r.Body != nil {
		defer r.Body.Close()
		payload, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(string(payload)) != "" {
			if err := json.Unmarshal(payload, &request); err != nil {
				writeError(w, http.StatusBadRequest, err)
				return
			}
		}
	}

	response, err := h.status.CheckDomainHealth(r.Context(), request.Domains)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	writeJSON(w, http.StatusOK, response)
}

func buildProvider(id string, req providerRequest) (config.Provider, error) {
	providerType := config.ProviderType(strings.TrimSpace(strings.ToLower(req.Type)))
	switch providerType {
	case config.ProviderTypeOpenVPN, config.ProviderTypeSubscription:
	default:
		return config.Provider{}, errors.New("provider type must be openvpn or subscription")
	}

	source := strings.TrimSpace(req.Source)
	if source == "" {
		return config.Provider{}, errors.New("provider source is required")
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = providerNameFromSource(source)
	}
	if name == "" {
		return config.Provider{}, errors.New("provider name is required")
	}

	if id == "" {
		id = newID("provider")
	}

	return config.Provider{
		ID:               id,
		Name:             name,
		Type:             providerType,
		Source:           source,
		SelectedLocation: strings.TrimSpace(req.SelectedLocation),
		Enabled:          req.Enabled,
	}, nil
}

func providerNameFromSource(source string) string {
	parsed, err := url.Parse(strings.TrimSpace(source))
	if err != nil {
		return ""
	}
	name, err := url.QueryUnescape(strings.TrimSpace(parsed.Fragment))
	if err != nil {
		return strings.TrimSpace(parsed.Fragment)
	}
	return strings.TrimSpace(name)
}

func shiftRoutingSettings(settings config.RoutingSettings, offset int) config.RoutingSettings {
	if offset <= 0 {
		return settings
	}

	settings.TableNum += offset
	settings.FWMark = incrementMark(settings.FWMark, offset)
	return settings
}

func incrementMark(base string, offset int) string {
	base = strings.TrimSpace(base)
	if base == "" {
		base = "0x1"
	}

	if strings.HasPrefix(strings.ToLower(base), "0x") {
		value, err := strconv.ParseInt(base[2:], 16, 64)
		if err != nil {
			value = 1
		}
		return fmt.Sprintf("0x%x", value+int64(offset))
	}

	value, err := strconv.Atoi(base)
	if err != nil {
		value = 1
	}
	return strconv.Itoa(value + offset)
}

func buildRule(id string, req ruleRequest, providers []config.Provider) (config.Rule, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return config.Rule{}, errors.New("rule name is required")
	}

	providerID := strings.TrimSpace(req.ProviderID)
	if providerID == "" {
		return config.Rule{}, errors.New("providerId is required")
	}
	if findProviderIndex(providers, providerID) < 0 {
		return config.Rule{}, fmt.Errorf("provider %s not found", providerID)
	}

	domainsList := splitDomains(req.Domains)

	if id == "" {
		id = newID("rule")
	}

	return config.Rule{
		ID:               id,
		Name:             name,
		ProviderID:       providerID,
		SelectedLocation: strings.TrimSpace(req.SelectedLocation),
		Domains:          domainsList,
		Enabled:          req.Enabled,
	}, nil
}

func (h *Handler) buildPriorityPolicy(id string, req priorityPolicyRequest, state config.State) (config.PriorityPolicy, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return config.PriorityPolicy{}, errors.New("priority policy name is required")
	}
	providerID := strings.TrimSpace(req.ProviderID)
	if providerID == "" {
		return config.PriorityPolicy{}, errors.New("providerId is required")
	}
	provider, exists := findProviderByID(state.Providers, providerID)
	if !exists {
		return config.PriorityPolicy{}, fmt.Errorf("provider %s not found", providerID)
	}
	if provider.Type != config.ProviderTypeSubscription {
		return config.PriorityPolicy{}, errors.New("priority policies are supported only for subscription providers")
	}

	targets := normalizePriorityRequestTargets(req.Targets)
	if len(targets) == 0 {
		return config.PriorityPolicy{}, errors.New("priority policy targets are required")
	}
	schedule, err := normalizePriorityRequestSchedule(req.Schedule, targets)
	if err != nil {
		return config.PriorityPolicy{}, err
	}
	if err := validatePriorityLocations(provider, targets, h.subscriptionRuntimeDir()); err != nil {
		return config.PriorityPolicy{}, err
	}
	if err := validatePrioritySchedule(schedule); err != nil {
		return config.PriorityPolicy{}, err
	}

	if id == "" {
		id = newID("policy")
	}
	return config.PriorityPolicy{
		ID:         id,
		ProviderID: providerID,
		Name:       name,
		Enabled:    req.Enabled,
		Entries:    nil,
		Targets:    targets,
		Schedule:   schedule,
	}, nil
}

func normalizePriorityRequestTargets(targets []config.PriorityTarget) []config.PriorityTarget {
	out := make([]config.PriorityTarget, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		location := strings.TrimSpace(target.Location)
		if location == "" {
			continue
		}
		key := strings.ToLower(location)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, config.PriorityTarget{Location: location})
	}
	return out
}

func normalizePriorityRequestSchedule(schedule []config.PriorityScheduleWindow, targets []config.PriorityTarget) ([]config.PriorityScheduleWindow, error) {
	out := make([]config.PriorityScheduleWindow, 0, len(schedule))
	for _, window := range schedule {
		start := strings.TrimSpace(window.Start)
		end := strings.TrimSpace(window.End)
		location := strings.TrimSpace(window.Location)
		if start == "" && end == "" && location == "" {
			continue
		}
		if start == "" || end == "" || location == "" {
			return nil, errors.New("priority schedule windows require start, end, and location")
		}
		if _, ok := parseClockMinute(start); !ok {
			return nil, fmt.Errorf("priority schedule start %q must use HH:MM", start)
		}
		if _, ok := parseClockMinute(end); !ok {
			return nil, fmt.Errorf("priority schedule end %q must use HH:MM", end)
		}
		if start == end {
			return nil, fmt.Errorf("priority schedule window %s-%s must not be empty", start, end)
		}
		if !targetLocationsContain(targets, location) {
			return nil, fmt.Errorf("priority schedule location %q is not in targets", location)
		}
		out = append(out, config.PriorityScheduleWindow{Start: start, End: end, Location: location})
	}
	return out, nil
}

func validatePriorityLocations(provider config.Provider, targets []config.PriorityTarget, runtimeDir string) error {
	entries, _, err := subscription.FetchEntriesCached(provider.Source, runtimeDir)
	if err != nil {
		return fmt.Errorf("load subscription locations for %q: %w", provider.Name, err)
	}
	available := make(map[string]string, len(entries))
	for _, entry := range entries {
		available[strings.ToLower(strings.TrimSpace(entry.Name))] = entry.Name
	}
	for _, target := range targets {
		key := strings.ToLower(strings.TrimSpace(target.Location))
		if _, exists := available[key]; !exists {
			return fmt.Errorf("location %q not found in provider %q", target.Location, provider.Name)
		}
	}
	return nil
}

func validatePrioritySchedule(schedule []config.PriorityScheduleWindow) error {
	segments := make([]clockSegment, 0, len(schedule)*2)
	for _, window := range schedule {
		start, okStart := parseClockMinute(window.Start)
		end, okEnd := parseClockMinute(window.End)
		if !okStart || !okEnd || start == end {
			return fmt.Errorf("priority schedule window %s-%s is invalid", window.Start, window.End)
		}
		for _, segment := range splitClockWindow(start, end) {
			for _, existing := range segments {
				if clockSegmentsOverlap(existing, segment) {
					return fmt.Errorf("priority schedule window %s-%s overlaps with %s-%s", window.Start, window.End, existing.startText, existing.endText)
				}
			}
			segment.startText = window.Start
			segment.endText = window.End
			segments = append(segments, segment)
		}
	}
	return nil
}

type clockSegment struct {
	start     int
	end       int
	startText string
	endText   string
}

func splitClockWindow(start int, end int) []clockSegment {
	if start < end {
		return []clockSegment{{start: start, end: end}}
	}
	return []clockSegment{{start: start, end: 24 * 60}, {start: 0, end: end}}
}

func clockSegmentsOverlap(left clockSegment, right clockSegment) bool {
	return left.start < right.end && right.start < left.end
}

func parseClockMinute(value string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) != 2 {
		return 0, false
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, false
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, false
	}
	if len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, false
	}
	return hour*60 + minute, true
}

func targetLocationsContain(targets []config.PriorityTarget, location string) bool {
	for _, target := range targets {
		if strings.EqualFold(strings.TrimSpace(target.Location), strings.TrimSpace(location)) {
			return true
		}
	}
	return false
}

func findProviderByID(providers []config.Provider, id string) (config.Provider, bool) {
	for _, provider := range providers {
		if provider.ID == id {
			return provider, true
		}
	}
	return config.Provider{}, false
}

func findPriorityPolicy(policies []config.PriorityPolicy, id string) (config.PriorityPolicy, bool) {
	for _, policy := range policies {
		if policy.ID == id {
			return policy, true
		}
	}
	return config.PriorityPolicy{}, false
}

func findPriorityPolicyIndex(policies []config.PriorityPolicy, id string) int {
	for index, policy := range policies {
		if policy.ID == id {
			return index
		}
	}
	return -1
}

func priorityPolicyHasTarget(policy config.PriorityPolicy, location string) bool {
	return targetLocationsContain(policy.Targets, location)
}

func (h *Handler) subscriptionRuntimeDir() string {
	if strings.TrimSpace(h.dataDir) == "" {
		return ""
	}
	return filepath.Join(h.dataDir, ".vpn-manager", "subscriptions")
}

type activeRuleOwner struct {
	RuleID   string
	RuleName string
	Label    string
}

type activeIPRange struct {
	entry  string
	prefix netip.Prefix
	owner  activeRuleOwner
}

func validateRuleEntries(candidate config.Rule, providers []config.Provider, existingRules []config.Rule) error {
	if !candidate.Enabled {
		return nil
	}

	providersByID := providersIndex(providers)
	provider, exists := providersByID[candidate.ProviderID]
	if !exists || !provider.Enabled {
		return nil
	}

	seen := make(map[string]activeRuleOwner)
	ipRanges := make([]activeIPRange, 0, 8)

	for _, existing := range existingRules {
		if existing.ID == candidate.ID || !existing.Enabled {
			continue
		}
		existingProvider, exists := providersByID[existing.ProviderID]
		if !exists || !existingProvider.Enabled {
			continue
		}
		if err := registerActiveEntries(
			existing.Domains,
			activeRuleOwner{RuleID: existing.ID, RuleName: existing.Name, Label: formatRuleLabel(existing, existingProvider)},
			seen,
			&ipRanges,
		); err != nil {
			return err
		}
	}

	return registerActiveEntries(
		candidate.Domains,
		activeRuleOwner{RuleID: candidate.ID, RuleName: candidate.Name, Label: formatRuleLabel(candidate, provider)},
		seen,
		&ipRanges,
	)
}

func validateProviderActivation(candidate config.Provider, providers []config.Provider, rules []config.Rule) error {
	if !candidate.Enabled {
		return nil
	}

	providersByID := providersIndex(providers)
	providersByID[candidate.ID] = candidate

	seen := make(map[string]activeRuleOwner)
	ipRanges := make([]activeIPRange, 0, 8)
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		provider, exists := providersByID[rule.ProviderID]
		if !exists || !provider.Enabled {
			continue
		}

		owner := activeRuleOwner{
			RuleID:   rule.ID,
			RuleName: rule.Name,
			Label:    formatRuleLabel(rule, provider),
		}
		if err := registerActiveEntries(rule.Domains, owner, seen, &ipRanges); err != nil {
			return err
		}
	}
	return nil
}

func validateActiveRuleEntries(state config.State) error {
	providersByID := providersIndex(state.Providers)
	seen := make(map[string]activeRuleOwner)
	ipRanges := make([]activeIPRange, 0, 8)

	for _, rule := range state.Rules {
		if !rule.Enabled {
			continue
		}
		provider, exists := providersByID[rule.ProviderID]
		if !exists || !provider.Enabled {
			continue
		}

		owner := activeRuleOwner{
			RuleID:   rule.ID,
			RuleName: rule.Name,
			Label:    formatRuleLabel(rule, provider),
		}
		if err := registerActiveEntries(rule.Domains, owner, seen, &ipRanges); err != nil {
			return err
		}
	}

	return nil
}

func validatePriorityPolicyState(state config.State) error {
	providersByID := providersIndex(state.Providers)
	enabledByProvider := make(map[string]config.PriorityPolicy)
	for _, policy := range state.PriorityPolicies {
		if !policy.Enabled {
			continue
		}
		providerID := strings.TrimSpace(policy.ProviderID)
		if providerID == "" {
			continue
		}
		if previous, exists := enabledByProvider[providerID]; exists {
			providerName := providerID
			if provider, ok := providersByID[providerID]; ok && strings.TrimSpace(provider.Name) != "" {
				providerName = provider.Name
			}
			return fmt.Errorf("only one enabled priority policy is allowed for provider %q: %q and %q", providerName, previous.Name, policy.Name)
		}
		enabledByProvider[providerID] = policy
	}
	return nil
}

func providersIndex(providers []config.Provider) map[string]config.Provider {
	byID := make(map[string]config.Provider, len(providers))
	for _, provider := range providers {
		byID[provider.ID] = provider
	}
	return byID
}

func formatRuleLabel(rule config.Rule, provider config.Provider) string {
	name := strings.TrimSpace(rule.SelectedLocation)
	if name == "" {
		name = strings.TrimSpace(rule.Name)
	}
	if providerName := strings.TrimSpace(provider.Name); providerName != "" {
		if name != "" {
			return providerName + " / " + name
		}
		return providerName
	}
	if name != "" {
		return name
	}
	if strings.TrimSpace(rule.ID) != "" {
		return rule.ID
	}
	return "unknown route"
}

func formatPriorityPolicyLabel(policy config.PriorityPolicy, provider config.Provider) string {
	name := strings.TrimSpace(policy.Name)
	if providerName := strings.TrimSpace(provider.Name); providerName != "" {
		if name != "" {
			return providerName + " / " + name
		}
		return providerName
	}
	if name != "" {
		return name
	}
	if strings.TrimSpace(policy.ID) != "" {
		return policy.ID
	}
	return "unknown priority policy"
}

func registerActiveEntries(entries []string, owner activeRuleOwner, seen map[string]activeRuleOwner, ipRanges *[]activeIPRange) error {
	for _, entry := range entries {
		if previous, exists := seen[entry]; exists && previous.RuleID != owner.RuleID {
			return duplicateEntryError(entry, previous, owner)
		}
		if prefix, ok := domains.ParseIPPrefix(entry); ok {
			for _, existing := range *ipRanges {
				if existing.owner.RuleID == owner.RuleID {
					continue
				}
				if prefixesOverlap(existing.prefix, prefix) {
					return overlappingEntryError(entry, existing.entry, existing.owner, owner)
				}
			}
			*ipRanges = append(*ipRanges, activeIPRange{
				entry:  entry,
				prefix: prefix,
				owner:  owner,
			})
		}
		seen[entry] = owner
	}

	return nil
}

func prefixesOverlap(left netip.Prefix, right netip.Prefix) bool {
	return left.Contains(right.Addr()) || right.Contains(left.Addr())
}

func duplicateEntryError(entry string, left activeRuleOwner, right activeRuleOwner) error {
	return fmt.Errorf("entry %q is already assigned to %q and cannot also be used in %q", entry, left.Label, right.Label)
}

func overlappingEntryError(entry string, existing string, left activeRuleOwner, right activeRuleOwner) error {
	return fmt.Errorf("entry %q overlaps with %q already assigned to %q and cannot also be used in %q", entry, existing, left.Label, right.Label)
}

func splitDomains(raw string) []string {
	return domains.SplitInput(raw)
}

func validateRoutingSettings(settings config.RoutingSettings) (config.RoutingSettings, error) {
	settings.VPNGateway = strings.TrimSpace(settings.VPNGateway)
	settings.VPNRouteMode = strings.ToLower(strings.TrimSpace(settings.VPNRouteMode))
	settings.LANIface = strings.TrimSpace(settings.LANIface)
	settings.VPNIface = strings.TrimSpace(settings.VPNIface)
	settings.FWZoneChain = strings.TrimSpace(settings.FWZoneChain)
	settings.IPSetName = strings.TrimSpace(settings.IPSetName)
	settings.FWMark = strings.TrimSpace(settings.FWMark)
	settings.DNSMasqConfigFile = strings.TrimSpace(settings.DNSMasqConfigFile)
	settings.IPv6Mode = strings.ToLower(strings.TrimSpace(settings.IPv6Mode))
	settings.LoadProfile = config.NormalizeRoutingLoadProfile(settings.LoadProfile)

	switch settings.VPNRouteMode {
	case "gateway", "dev":
	default:
		return config.RoutingSettings{}, errors.New("vpnRouteMode must be gateway or dev")
	}

	if settings.VPNRouteMode == "gateway" && settings.VPNGateway == "" {
		return config.RoutingSettings{}, errors.New("vpnGateway is required in gateway mode")
	}
	if settings.LANIface == "" {
		return config.RoutingSettings{}, errors.New("lanIface is required")
	}
	if settings.VPNIface == "" {
		return config.RoutingSettings{}, errors.New("vpnIface is required")
	}
	if settings.TableNum <= 0 {
		return config.RoutingSettings{}, errors.New("tableNum must be greater than 0")
	}
	if settings.FWZoneChain == "" {
		return config.RoutingSettings{}, errors.New("fwZoneChain is required")
	}
	if settings.IPSetName == "" {
		return config.RoutingSettings{}, errors.New("ipSetName is required")
	}
	if settings.FWMark == "" {
		return config.RoutingSettings{}, errors.New("fwMark is required")
	}
	if settings.DNSMasqConfigFile == "" {
		return config.RoutingSettings{}, errors.New("dnsMasqConfigFile is required")
	}
	if settings.MSSValue < 0 || settings.MSSValue > 1460 {
		return config.RoutingSettings{}, errors.New("mssValue must be between 0 and 1460")
	}
	switch settings.IPv6Mode {
	case "", "warn":
		settings.IPv6Mode = "warn"
	case "allow", "disable":
		// accepted
	default:
		return config.RoutingSettings{}, errors.New("ipv6Mode must be warn, allow, or disable")
	}
	switch settings.LoadProfile {
	case config.RoutingLoadProfileMinimal, config.RoutingLoadProfileNormal, config.RoutingLoadProfileDetailed:
	default:
		return config.RoutingSettings{}, errors.New("loadProfile must be minimal, normal, or detailed")
	}

	return settings, nil
}

func extractID(path string, prefix string) (string, error) {
	rawID := strings.TrimPrefix(path, prefix)
	if rawID == "" || rawID == path {
		return "", errors.New("id is required")
	}

	id, err := url.PathUnescape(rawID)
	if err != nil {
		return "", err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errors.New("id is required")
	}
	return id, nil
}

func extractProviderAction(path string) (string, string, error) {
	raw := strings.TrimPrefix(path, "/api/providers/")
	if raw == "" || raw == path {
		return "", "", errors.New("id is required")
	}

	parts := strings.Split(strings.Trim(raw, "/"), "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		return "", "", errors.New("id is required")
	}
	if len(parts) > 2 {
		return "", "", fmt.Errorf("provider path %q not found", path)
	}

	id, err := url.PathUnescape(parts[0])
	if err != nil {
		return "", "", err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return "", "", errors.New("id is required")
	}

	action := ""
	if len(parts) == 2 {
		action = strings.TrimSpace(parts[1])
	}
	return id, action, nil
}

func findProviderIndex(providers []config.Provider, id string) int {
	for index, provider := range providers {
		if provider.ID == id {
			return index
		}
	}
	return -1
}

func providerHasEnabledRules(state config.State, providerID string) bool {
	for _, rule := range state.Rules {
		if rule.ProviderID == providerID && rule.Enabled {
			return true
		}
	}
	return false
}

func findRuleIndex(rules []config.Rule, id string) int {
	for index, rule := range rules {
		if rule.ID == id {
			return index
		}
	}
	return -1
}

func newID(prefix string) string {
	buf := make([]byte, 4)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return prefix + "_" + hex.EncodeToString(buf)
}

func decodeJSON(r *http.Request, target any) error {
	defer r.Body.Close()

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func parsePositiveQueryInt(values url.Values, key string, fallback int) int {
	raw := strings.TrimSpace(values.Get(key))
	if raw == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func parsePositiveQueryIntWithLegacy(values url.Values, primaryKey string, legacyKey string, fallback int) int {
	if raw := strings.TrimSpace(values.Get(primaryKey)); raw != "" {
		return parsePositiveQueryInt(values, primaryKey, fallback)
	}
	if legacyKey == "" {
		return fallback
	}
	return parsePositiveQueryInt(values, legacyKey, fallback)
}

func truthyQueryValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, statusCode int, err error) {
	writeJSON(w, statusCode, map[string]string{
		"error": err.Error(),
	})
}

func writeUpdateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, update.ErrOperationInProgress):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, update.ErrUnsupportedRuntime):
		writeError(w, http.StatusServiceUnavailable, err)
	case errors.Is(err, update.ErrInvalidBundle), errors.Is(err, update.ErrReleaseAssetNotFound):
		writeError(w, http.StatusBadRequest, err)
	default:
		writeError(w, http.StatusInternalServerError, err)
	}
}

func writeMethodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	writeError(w, http.StatusMethodNotAllowed, errors.New("method is not allowed"))
}

func (h *Handler) recordEvent(level string, kind string, message string) {
	if h.events == nil {
		return
	}
	_, _ = h.events.Add(level, kind, message)
}
