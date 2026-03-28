package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"xiomi-router-driver/internal/automation"
	"xiomi-router-driver/internal/blacklist"
	"xiomi-router-driver/internal/config"
	"xiomi-router-driver/internal/domains"
	"xiomi-router-driver/internal/events"
	"xiomi-router-driver/internal/openvpn"
	"xiomi-router-driver/internal/probe"
	"xiomi-router-driver/internal/routing"
	"xiomi-router-driver/internal/status"
	"xiomi-router-driver/internal/subscription"
)

type Dependencies struct {
	State           *config.Manager
	Domains         *domains.Manager
	Events          *events.Store
	Routing         *routing.Runner
	Automation      *automation.Manager
	OpenVPN         *openvpn.Manager
	Subscriptions   *subscription.Manager
	Status          *status.Service
	Blacklist       *blacklist.Manager
	BlacklistRunner *blacklist.Runner
	DataDir         string
}

type Handler struct {
	state           *config.Manager
	domains         *domains.Manager
	events          *events.Store
	routing         *routing.Runner
	automation      *automation.Manager
	openvpn         *openvpn.Manager
	subscriptions   *subscription.Manager
	status          *status.Service
	blacklist       *blacklist.Manager
	blacklistRunner *blacklist.Runner
	dataDir         string
	router          http.Handler
	applyMu         sync.Mutex
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

type applyResult struct {
	Status       string   `json:"status"`
	RulesApplied int      `json:"rulesApplied"`
	Domains      []string `json:"domains"`
}

func NewHandler(deps Dependencies) *Handler {
	handler := &Handler{
		state:           deps.State,
		domains:         deps.Domains,
		events:          deps.Events,
		routing:         deps.Routing,
		automation:      deps.Automation,
		openvpn:         deps.OpenVPN,
		subscriptions:   deps.Subscriptions,
		status:          deps.Status,
		blacklist:       deps.Blacklist,
		blacklistRunner: deps.BlacklistRunner,
		dataDir:         deps.DataDir,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", handler.handleStatus)
	mux.HandleFunc("/api/traffic/history", handler.handleTrafficHistory)
	mux.HandleFunc("/api/traffic/domains", handler.handleDomainTraffic)
	mux.HandleFunc("/api/traffic/sites/history", handler.handleSiteTrafficHistory)
	mux.HandleFunc("/api/traffic/sites", handler.handleSiteTraffic)
	mux.HandleFunc("/api/traffic/devices/history", handler.handleDeviceTrafficHistory)
	mux.HandleFunc("/api/traffic/devices", handler.handleDeviceTraffic)
	mux.HandleFunc("/api/config", handler.handleConfig)
	mux.HandleFunc("/api/config/routing", handler.handleRoutingConfig)
	mux.HandleFunc("/api/config/automation", handler.handleAutomationConfig)
	mux.HandleFunc("/api/events", handler.handleEvents)
	mux.HandleFunc("/api/providers/probe", handler.handleProbeProvider)
	mux.HandleFunc("/api/providers/latency", handler.handleProviderLatency)
	mux.HandleFunc("/api/providers/upload", handler.handleUploadProfile)
	mux.HandleFunc("/api/providers", handler.handleProviders)
	mux.HandleFunc("/api/providers/", handler.handleProvider)
	mux.HandleFunc("/api/rules", handler.handleRules)
	mux.HandleFunc("/api/rules/", handler.handleRule)
	mux.HandleFunc("/api/rules/apply", handler.handleApplyRules)
	mux.HandleFunc("/api/domains", handler.handleDomainsPreview)
	mux.HandleFunc("/api/blacklist", handler.handleBlacklist)
	mux.HandleFunc("/api/blacklist/apply", handler.handleApplyBlacklist)
	mux.HandleFunc("/api/system/resources", handler.handleSystemResources)
	handler.router = mux
	return handler
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

		result, err := h.status.DomainTraffic(sortBy, limit)
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
		scope := q.Get("scope")
		search := q.Get("query")
		sourceIP := q.Get("sourceIp")
		pageSize := parsePositiveQueryIntWithLegacy(q, "pageSize", "limit", 20)
		page := parsePositiveQueryInt(q, "page", 1)

		result, err := h.status.SiteTraffic(scope, sortBy, sourceIP, search, page, pageSize)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusOK, result)

	case http.MethodPost:
		q := r.URL.Query()
		sortBy := q.Get("sort")
		scope := q.Get("scope")
		search := q.Get("query")
		sourceIP := q.Get("sourceIp")
		pageSize := parsePositiveQueryIntWithLegacy(q, "pageSize", "limit", 20)
		page := parsePositiveQueryInt(q, "page", 1)

		if err := h.status.SampleSiteTraffic(); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		result, err := h.status.SiteTraffic(scope, sortBy, sourceIP, search, page, pageSize)
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
			result, err = h.status.SiteTrafficHistoryCustom(scope, sortBy, sourceIP, search, page, pageSize, fromStr, toStr)
		} else {
			result, err = h.status.SiteTrafficHistory(scope, sortBy, sourceIP, search, page, pageSize, q.Get("range"))
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

		result, err := h.status.DeviceTraffic(scope, sortBy, sourceIP, search, page, pageSize, siteLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusOK, result)

	case http.MethodPost:
		q := r.URL.Query()
		sortBy := q.Get("sort")
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

		result, err := h.status.DeviceTraffic(scope, sortBy, sourceIP, search, page, pageSize, siteLimit)
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
	id, err := extractID(r.URL.Path, "/api/providers/")
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
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
			if err := validateProviderActivation(provider, state.Providers, state.Rules); err != nil {
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

		state.Providers = nextProviders
		state.Rules = nextRules
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
		if err := validateRuleDomains(rule, state.Providers, state.Rules); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		state.Rules = append(state.Rules, rule)
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
		if err := validateRuleDomains(rule, state.Providers, state.Rules); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}

		state.Rules[index] = rule
		if _, err := h.state.Save(state); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{"rule": rule})
		h.recordEvent("info", "rule.updated", fmt.Sprintf("Rule %q updated", rule.Name))
	case http.MethodDelete:
		state, err := h.state.Load()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}

		nextRules := make([]config.Rule, 0, len(state.Rules))
		found := false
		for _, rule := range state.Rules {
			if rule.ID == id {
				found = true
				continue
			}
			nextRules = append(nextRules, rule)
		}
		if !found {
			writeError(w, http.StatusNotFound, fmt.Errorf("rule %s not found", id))
			return
		}

		state.Rules = nextRules
		if _, err := h.state.Save(state); err != nil {
			writeError(w, http.StatusInternalServerError, err)
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

	result, err := h.applyCurrentRules(r.Context())
	if err != nil {
		statusCode := http.StatusInternalServerError
		switch {
		case strings.Contains(err.Error(), "not implemented yet"):
			statusCode = http.StatusConflict
		case strings.Contains(err.Error(), "is not configured"):
			statusCode = http.StatusInternalServerError
		}
		writeError(w, statusCode, err)
		return
	}

	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) ApplyCurrentRules(ctx context.Context) error {
	_, err := h.applyCurrentRules(ctx)
	return err
}

func (h *Handler) applyCurrentRules(ctx context.Context) (applyResult, error) {
	h.applyMu.Lock()
	defer h.applyMu.Unlock()

	state, err := h.state.Load()
	if err != nil {
		return applyResult{}, err
	}
	if err := validateActiveRuleDomains(state); err != nil {
		state.LastError = err.Error()
		_, _ = h.state.Save(state)
		h.recordEvent("error", "rules.apply_failed", err.Error())
		return applyResult{}, err
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

	if err := h.domains.ReplaceAll(domainsToApply); err != nil {
		state.LastError = err.Error()
		_, _ = h.state.Save(state)
		h.recordEvent("error", "rules.apply_failed", err.Error())
		return applyResult{}, err
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
			err := errors.Join(cleanupErrors...)
			state.LastError = err.Error()
			_, _ = h.state.Save(state)
			h.recordEvent("error", "rules.apply_failed", err.Error())
			return applyResult{}, err
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
			err := errors.New("simultaneous apply for multiple openvpn providers is not implemented yet")
			state.LastError = err.Error()
			_, _ = h.state.Save(state)
			h.recordEvent("error", "rules.apply_failed", err.Error())
			return applyResult{}, err
		}

		if len(openvpnRules) > 0 {
			if h.openvpn == nil {
				err := errors.New("openvpn runtime manager is not configured")
				state.LastError = err.Error()
				_, _ = h.state.Save(state)
				h.recordEvent("error", "rules.apply_failed", err.Error())
				return applyResult{}, err
			}
			if err := h.openvpn.Apply(ctx, openvpnProvider, openvpnDomains, state.Routing); err != nil {
				state.LastError = err.Error()
				_, _ = h.state.Save(state)
				h.recordEvent("error", "rules.apply_failed", err.Error())
				return applyResult{}, err
			}
		} else if h.openvpn != nil {
			if err := h.openvpn.Cleanup(ctx); err != nil {
				state.LastError = err.Error()
				_, _ = h.state.Save(state)
				h.recordEvent("error", "rules.apply_failed", err.Error())
				return applyResult{}, err
			}
		} else {
			if err := h.routing.Run(ctx, "del", state.Routing); err != nil {
				state.LastError = err.Error()
				_, _ = h.state.Save(state)
				h.recordEvent("error", "rules.apply_failed", err.Error())
				return applyResult{}, err
			}
		}

		if len(subscriptionRules) > 0 {
			if h.subscriptions == nil {
				if h.openvpn != nil {
					_ = h.openvpn.Cleanup(ctx)
				}
				err := errors.New("subscription runtime manager is not configured")
				state.LastError = err.Error()
				_, _ = h.state.Save(state)
				h.recordEvent("error", "rules.apply_failed", err.Error())
				return applyResult{}, err
			}
			subscriptionState := state
			if len(openvpnRules) > 0 {
				subscriptionState.Routing = shiftRoutingSettings(subscriptionState.Routing, 1)
			}
			if err := h.subscriptions.Apply(ctx, subscriptionState, subscriptionRules); err != nil {
				if h.openvpn != nil {
					_ = h.openvpn.Cleanup(ctx)
				}
				state.LastError = err.Error()
				_, _ = h.state.Save(state)
				h.recordEvent("error", "rules.apply_failed", err.Error())
				return applyResult{}, err
			}
		} else if h.subscriptions != nil {
			if err := h.subscriptions.Cleanup(ctx); err != nil {
				if h.openvpn != nil {
					_ = h.openvpn.Cleanup(ctx)
				}
				state.LastError = err.Error()
				_, _ = h.state.Save(state)
				h.recordEvent("error", "rules.apply_failed", err.Error())
				return applyResult{}, err
			}
		}
	}

	state.LastAppliedAt = time.Now().UTC().Format(time.RFC3339)
	state.LastError = ""
	_, _ = h.state.Save(state)

	h.recordEvent("info", "rules.applied", fmt.Sprintf("Applied %d rules for %d domains", len(enabledRules), len(domainsToApply)))
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

func (h *Handler) handleBlacklist(w http.ResponseWriter, r *http.Request) {
	if h.blacklist == nil {
		writeError(w, http.StatusNotImplemented, errors.New("blacklist is not configured"))
		return
	}

	switch r.Method {
	case http.MethodGet:
		entries, err := h.blacklist.List()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		domainCount, ipCount := 0, 0
		for _, e := range entries {
			if e.Type == "ip" {
				ipCount++
			} else {
				domainCount++
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"entries":     entries,
			"count":       len(entries),
			"domainCount": domainCount,
			"ipCount":     ipCount,
		})

	case http.MethodPost:
		var body struct {
			Entries string `json:"entries"`
		}
		if err := decodeJSON(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		values := splitDomains(body.Entries)
		if len(values) == 0 {
			writeError(w, http.StatusBadRequest, errors.New("no valid entries provided"))
			return
		}
		if err := h.blacklist.AddMany(values); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		entries, _ := h.blacklist.List()
		domainCount, ipCount := 0, 0
		for _, e := range entries {
			if e.Type == "ip" {
				ipCount++
			} else {
				domainCount++
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"entries":     entries,
			"count":       len(entries),
			"domainCount": domainCount,
			"ipCount":     ipCount,
		})
		h.recordEvent("info", "blacklist.updated", fmt.Sprintf("Blacklist updated: %d domains, %d IPs", domainCount, ipCount))

	case http.MethodDelete:
		value := r.URL.Query().Get("value")
		if value == "" {
			writeError(w, http.StatusBadRequest, errors.New("value parameter is required"))
			return
		}
		if err := h.blacklist.Delete(value); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		entries, _ := h.blacklist.List()
		writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "count": len(entries)})
		h.recordEvent("info", "blacklist.entry_removed", fmt.Sprintf("Removed %s from blacklist", value))

	default:
		writeMethodNotAllowed(w, http.MethodGet, http.MethodPost, http.MethodDelete)
	}
}

func (h *Handler) handleApplyBlacklist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}
	if h.blacklist == nil || h.blacklistRunner == nil {
		writeError(w, http.StatusNotImplemented, errors.New("blacklist is not configured"))
		return
	}

	state, err := h.state.Load()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	entries, err := h.blacklist.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	opts := blacklist.RunOptions{
		DomainsListPath: h.blacklist.DomainsListPath(),
		IPsListPath:     h.blacklist.IPsListPath(),
		LANIface:        state.Routing.LANIface,
	}

	action := "add"
	if len(entries) == 0 {
		action = "del"
	}

	if err := h.blacklistRunner.Run(r.Context(), action, opts); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		h.recordEvent("error", "blacklist.apply_failed", err.Error())
		return
	}

	domainCount, ipCount := 0, 0
	for _, e := range entries {
		if e.Type == "ip" {
			ipCount++
		} else {
			domainCount++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "applied",
		"domainCount": domainCount,
		"ipCount":     ipCount,
	})
	h.recordEvent("info", "blacklist.applied", fmt.Sprintf("Blacklist applied: %d domains, %d IPs", domainCount, ipCount))
}

func buildProvider(id string, req providerRequest) (config.Provider, error) {
	providerType := config.ProviderType(strings.TrimSpace(strings.ToLower(req.Type)))
	switch providerType {
	case config.ProviderTypeOpenVPN, config.ProviderTypeSubscription:
	default:
		return config.Provider{}, errors.New("provider type must be openvpn or subscription")
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		return config.Provider{}, errors.New("provider name is required")
	}

	source := strings.TrimSpace(req.Source)
	if source == "" {
		return config.Provider{}, errors.New("provider source is required")
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

type activeRuleOwner struct {
	RuleID   string
	RuleName string
	Label    string
}

func validateRuleDomains(candidate config.Rule, providers []config.Provider, existingRules []config.Rule) error {
	if !candidate.Enabled {
		return nil
	}

	providersByID := providersIndex(providers)
	provider, exists := providersByID[candidate.ProviderID]
	if !exists || !provider.Enabled {
		return nil
	}

	candidateDomains := make(map[string]struct{}, len(candidate.Domains))
	for _, domain := range candidate.Domains {
		candidateDomains[domain] = struct{}{}
	}
	if len(candidateDomains) == 0 {
		return nil
	}

	for _, existing := range existingRules {
		if existing.ID == candidate.ID || !existing.Enabled {
			continue
		}
		existingProvider, exists := providersByID[existing.ProviderID]
		if !exists || !existingProvider.Enabled {
			continue
		}
		for _, domain := range existing.Domains {
			if _, exists := candidateDomains[domain]; exists {
				return duplicateDomainError(
					domain,
					activeRuleOwner{RuleID: existing.ID, RuleName: existing.Name, Label: formatRuleLabel(existing, existingProvider)},
					activeRuleOwner{RuleID: candidate.ID, RuleName: candidate.Name, Label: formatRuleLabel(candidate, provider)},
				)
			}
		}
	}

	return nil
}

func validateProviderActivation(candidate config.Provider, providers []config.Provider, rules []config.Rule) error {
	if !candidate.Enabled {
		return nil
	}

	providersByID := providersIndex(providers)
	providersByID[candidate.ID] = candidate

	seen := make(map[string]activeRuleOwner)
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
		for _, domain := range rule.Domains {
			if previous, exists := seen[domain]; exists && previous.RuleID != owner.RuleID {
				return duplicateDomainError(domain, previous, owner)
			}
			seen[domain] = owner
		}
	}

	return nil
}

func validateActiveRuleDomains(state config.State) error {
	providersByID := providersIndex(state.Providers)
	seen := make(map[string]activeRuleOwner)

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
		for _, domain := range rule.Domains {
			if previous, exists := seen[domain]; exists && previous.RuleID != owner.RuleID {
				return duplicateDomainError(domain, previous, owner)
			}
			seen[domain] = owner
		}
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

func duplicateDomainError(domain string, left activeRuleOwner, right activeRuleOwner) error {
	return fmt.Errorf("domain %q is already assigned to %q and cannot also be used in %q", domain, left.Label, right.Label)
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

func findProviderIndex(providers []config.Provider, id string) int {
	for index, provider := range providers {
		if provider.ID == id {
			return index
		}
	}
	return -1
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
