package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"xiomi-router-driver/internal/config"
)

var ErrOperationInProgress = errors.New("update operation is already running")
var ErrUnsupportedRuntime = errors.New("update install is only supported on Linux")

type Options struct {
	AppDir      string
	DataDir     string
	State       *config.Manager
	HTTPClient  *http.Client
	RecordEvent func(level string, kind string, message string)
	Restart     func()
	RuntimeOS   string
}

type Operation struct {
	Type       string `json:"type"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	Error      string `json:"error,omitempty"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

type Manager struct {
	appDir      string
	dataDir     string
	state       *config.Manager
	recordEvent func(level string, kind string, message string)
	httpClient  *http.Client
	restart     func()
	runtimeOS   string

	mu        sync.Mutex
	operation *Operation
	lastCheck *ReleaseCandidate
}

type ReleaseCandidate struct {
	TagName     string `json:"tagName"`
	Name        string `json:"name,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
	AssetName   string `json:"assetName"`
	AssetURL    string `json:"assetUrl"`
	AssetSize   int64  `json:"assetSize"`
}

type StatusResponse struct {
	Settings  config.UpdateSettings `json:"settings"`
	Installed *BundleInfo           `json:"installed,omitempty"`
	Latest    *ReleaseCandidate     `json:"latest,omitempty"`
	Operation *Operation            `json:"operation,omitempty"`
	Supported bool                  `json:"supported"`
}

type InstallResult struct {
	Status    string     `json:"status"`
	BackupDir string     `json:"backupDir,omitempty"`
	Bundle    BundleInfo `json:"bundle"`
	Restart   string     `json:"restart"`
}

func NewManager(options Options) *Manager {
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	restart := options.Restart
	if restart == nil {
		restart = func() {}
	}
	runtimeOS := strings.TrimSpace(options.RuntimeOS)
	if runtimeOS == "" {
		runtimeOS = runtime.GOOS
	}

	return &Manager{
		appDir:      options.AppDir,
		dataDir:     options.DataDir,
		state:       options.State,
		recordEvent: options.RecordEvent,
		httpClient:  client,
		restart:     restart,
		runtimeOS:   runtimeOS,
	}
}

func (m *Manager) Status(ctx context.Context) (StatusResponse, error) {
	settings, err := m.loadSettings()
	if err != nil {
		return StatusResponse{}, err
	}

	response := StatusResponse{
		Settings:  settings,
		Latest:    m.latestCandidate(),
		Operation: m.currentOperation(),
		Supported: m.runtimeOS == "linux",
	}

	if info, err := ParseBundleInfo(filepath.Join(m.appDir, "bundle-info.txt")); err == nil {
		info.Root = m.appDir
		response.Installed = &info
	} else if !errors.Is(err, os.ErrNotExist) {
		return StatusResponse{}, err
	}

	_ = ctx
	return response, nil
}

func (m *Manager) SaveSettings(ctx context.Context, settings config.UpdateSettings) (StatusResponse, error) {
	if m.state == nil {
		return StatusResponse{}, errors.New("state manager is not configured")
	}

	state, err := m.state.Load()
	if err != nil {
		return StatusResponse{}, err
	}
	state.Update = settings
	if _, err := m.state.Save(state); err != nil {
		return StatusResponse{}, err
	}
	return m.Status(ctx)
}

func (m *Manager) Check(ctx context.Context) (StatusResponse, error) {
	finish, err := m.beginOperation("check")
	if err != nil {
		return StatusResponse{}, err
	}

	settings, err := m.loadSettings()
	if err != nil {
		finish(err)
		return StatusResponse{}, err
	}
	candidate, err := m.checkRelease(ctx, settings)
	if err != nil {
		m.record("error", "update.check_failed", err.Error())
		finish(err)
		return StatusResponse{}, err
	}
	m.setLatestCandidate(candidate)
	m.record("info", "update.checked", fmt.Sprintf("Update candidate found: %s / %s", candidate.TagName, candidate.AssetName))
	finish(nil)
	return m.Status(ctx)
}

func (m *Manager) InstallLatest(ctx context.Context) (InstallResult, error) {
	if m.runtimeOS != "linux" {
		return InstallResult{}, ErrUnsupportedRuntime
	}

	finish, err := m.beginOperation("install")
	if err != nil {
		return InstallResult{}, err
	}
	defer func() {
		if err != nil {
			finish(err)
		} else {
			finish(nil)
		}
	}()

	settings, err := m.loadSettings()
	if err != nil {
		return InstallResult{}, err
	}
	candidate := m.latestCandidate()
	if candidate == nil {
		next, checkErr := m.checkRelease(ctx, settings)
		if checkErr != nil {
			err = checkErr
			m.record("error", "update.install_failed", err.Error())
			return InstallResult{}, err
		}
		candidate = &next
		m.setLatestCandidate(next)
	}

	archivePath, err := m.downloadArchive(ctx, candidate.AssetURL)
	if err != nil {
		m.record("error", "update.install_failed", err.Error())
		return InstallResult{}, err
	}
	result, err := m.installArchive(ctx, archivePath)
	if err != nil {
		m.record("error", "update.install_failed", err.Error())
		return InstallResult{}, err
	}
	m.record("info", "update.installed", fmt.Sprintf("Update installed from %s", candidate.AssetName))
	m.scheduleRestart()
	return result, nil
}

func (m *Manager) InstallUploaded(ctx context.Context, reader io.Reader, filename string) (InstallResult, error) {
	if m.runtimeOS != "linux" {
		return InstallResult{}, ErrUnsupportedRuntime
	}

	finish, err := m.beginOperation("upload")
	if err != nil {
		return InstallResult{}, err
	}
	defer func() {
		if err != nil {
			finish(err)
		} else {
			finish(nil)
		}
	}()

	archivePath, err := m.writeUploadedArchive(reader, filename)
	if err != nil {
		m.record("error", "update.upload_failed", err.Error())
		return InstallResult{}, err
	}
	result, err := m.installArchive(ctx, archivePath)
	if err != nil {
		m.record("error", "update.upload_failed", err.Error())
		return InstallResult{}, err
	}
	m.record("info", "update.upload_installed", fmt.Sprintf("Update installed from uploaded archive %s", filepath.Base(filename)))
	m.scheduleRestart()
	return result, nil
}

func (m *Manager) beginOperation(operationType string) (func(error), error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.operation != nil && m.operation.FinishedAt == "" {
		return nil, ErrOperationInProgress
	}

	now := time.Now().UTC().Format(time.RFC3339)
	operation := &Operation{
		Type:      operationType,
		Status:    "running",
		StartedAt: now,
	}
	m.operation = operation

	return func(err error) {
		m.mu.Lock()
		defer m.mu.Unlock()

		operation.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		if err != nil {
			operation.Status = "failed"
			operation.Error = err.Error()
			return
		}
		operation.Status = "succeeded"
	}, nil
}

func (m *Manager) currentOperation() *Operation {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.operation == nil {
		return nil
	}
	copy := *m.operation
	return &copy
}

func (m *Manager) latestCandidate() *ReleaseCandidate {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.lastCheck == nil {
		return nil
	}
	copy := *m.lastCheck
	return &copy
}

func (m *Manager) setLatestCandidate(candidate ReleaseCandidate) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.lastCheck = &candidate
}

func (m *Manager) loadSettings() (config.UpdateSettings, error) {
	if m.state == nil {
		return config.DefaultUpdateSettings(), nil
	}
	state, err := m.state.Load()
	if err != nil {
		return config.UpdateSettings{}, err
	}
	return state.Update, nil
}

func (m *Manager) checkRelease(ctx context.Context, settings config.UpdateSettings) (ReleaseCandidate, error) {
	repo := strings.Trim(settings.Repository, "/")
	if repo == "" || !strings.Contains(repo, "/") {
		return ReleaseCandidate{}, errors.New("update repository is invalid")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return ReleaseCandidate{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "vpn-manager-updater")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return ReleaseCandidate{}, fmt.Errorf("check GitHub release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ReleaseCandidate{}, errors.New("GitHub release not found")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ReleaseCandidate{}, fmt.Errorf("GitHub release check failed: HTTP %d", resp.StatusCode)
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return ReleaseCandidate{}, fmt.Errorf("decode GitHub release: %w", err)
	}
	asset, err := SelectAsset(release, settings.AssetPattern)
	if err != nil {
		return ReleaseCandidate{}, err
	}

	return ReleaseCandidate{
		TagName:     release.TagName,
		Name:        release.Name,
		PublishedAt: release.PublishedAt,
		AssetName:   asset.Name,
		AssetURL:    asset.BrowserDownloadURL,
		AssetSize:   asset.Size,
	}, nil
}

func (m *Manager) downloadArchive(ctx context.Context, url string) (string, error) {
	if strings.TrimSpace(url) == "" {
		return "", errors.New("release asset download URL is empty")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download update archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download update archive failed: HTTP %d", resp.StatusCode)
	}
	return m.writeArchive(resp.Body, "github.tar.gz")
}

func (m *Manager) writeUploadedArchive(reader io.Reader, filename string) (string, error) {
	name := filepath.Base(strings.TrimSpace(filename))
	if name == "." || name == string(filepath.Separator) || name == "" {
		name = "upload.tar.gz"
	}
	return m.writeArchive(reader, name)
}

func (m *Manager) writeArchive(reader io.Reader, filename string) (string, error) {
	dir, err := m.newUpdateWorkDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, filepath.Base(filename))
	output, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", fmt.Errorf("create update archive: %w", err)
	}
	if _, err := io.Copy(output, reader); err != nil {
		_ = output.Close()
		return "", fmt.Errorf("write update archive: %w", err)
	}
	if err := output.Close(); err != nil {
		return "", fmt.Errorf("close update archive: %w", err)
	}
	return path, nil
}

func (m *Manager) installArchive(ctx context.Context, archivePath string) (InstallResult, error) {
	_ = ctx

	extractDir := filepath.Join(filepath.Dir(archivePath), "extracted")
	if err := ExtractTarGz(archivePath, extractDir); err != nil {
		return InstallResult{}, err
	}
	info, err := ValidateBundle(extractDir)
	if err != nil {
		return InstallResult{}, err
	}
	backupDir, err := installBundle(m.appDir, m.dataDir, info, time.Now().UTC())
	if err != nil {
		return InstallResult{}, err
	}

	return InstallResult{
		Status:    "installed",
		BackupDir: backupDir,
		Bundle:    info,
		Restart:   "scheduled",
	}, nil
}

func (m *Manager) newUpdateWorkDir() (string, error) {
	base := filepath.Join(m.dataDir, ".vpn-manager", "updates")
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", fmt.Errorf("prepare update work directory: %w", err)
	}
	dir, err := os.MkdirTemp(base, "update-*")
	if err != nil {
		return "", fmt.Errorf("create update work directory: %w", err)
	}
	return dir, nil
}

func (m *Manager) scheduleRestart() {
	go func() {
		time.Sleep(500 * time.Millisecond)
		m.restart()
	}()
}

func (m *Manager) record(level string, kind string, message string) {
	if m.recordEvent == nil {
		return
	}
	m.recordEvent(level, kind, message)
}
