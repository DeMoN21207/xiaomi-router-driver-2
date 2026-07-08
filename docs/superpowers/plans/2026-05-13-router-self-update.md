# Router Self-Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Сделать обновление `vpn-manager` на роутере из публичного GitHub Release и через ручную загрузку `.tar.gz` архива из UI.

**Architecture:** Backend получает release metadata, скачивает или принимает архив, безопасно распаковывает его, валидирует bundle, делает backup текущих runtime-файлов без `data/`, заменяет файлы и планирует restart. UI в Settings показывает источник обновлений, текущий bundle, результат проверки, install/upload actions и состояние перезапуска.

**Tech Stack:** Go `net/http`, `archive/tar`, `compress/gzip`, existing config/events/api layers, React Settings page, existing `fetchJSON`, Vite build.

---

## File Map

- Create `internal/update/manager.go`: публичный update manager, operation lock, GitHub check, install/upload orchestration, status response.
- Create `internal/update/github.go`: GitHub release response structs and asset selection.
- Create `internal/update/archive.go`: safe `.tar.gz` extraction.
- Create `internal/update/bundle.go`: `bundle-info.txt` parsing and bundle validation.
- Create `internal/update/install.go`: backup, runtime file replacement, executable bits, delayed restart.
- Create `internal/update/update_test.go`: tests for asset selection, archive safety, validation, install preservation and operation conflict.
- Modify `internal/config/state.go`: add `UpdateSettings` with defaults and persistence normalization.
- Modify `internal/api/handler.go`: wire update dependencies and `/api/system/update*` endpoints.
- Modify `cmd/vpn-manager/main.go`: construct update manager with app/data dirs and event recorder.
- Modify `frontend/src/pages/SettingsPage.jsx`: add Updates section and actions.
- Modify `frontend/src/i18n.jsx`: add Russian and English update strings.
- Modify `package_router.sh` and `package_router.bat`: emit `version` or source metadata in `bundle-info.txt` if available.

## Task 1: Config Model For Update Settings

**Files:**
- Modify: `internal/config/state.go`

- [ ] **Step 1: Write failing config test**

Add a test in `internal/config/state_test.go`:

```go
func TestDefaultStateIncludesUpdateSettings(t *testing.T) {
	state := DefaultState()
	if state.Update.Repository != "DeMoN21207/xiaomi-router-driver-2" {
		t.Fatalf("repository = %q", state.Update.Repository)
	}
	if state.Update.AssetPattern != "vpn-manager-linux-arm64.tar.gz" {
		t.Fatalf("asset pattern = %q", state.Update.AssetPattern)
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `./.tools/go/bin/go test ./internal/config -run TestDefaultStateIncludesUpdateSettings`

Expected: compile failure because `State.Update` is not defined.

- [ ] **Step 3: Add minimal model**

Add:

```go
type UpdateSettings struct {
	Repository   string `json:"repository"`
	AssetPattern string `json:"assetPattern"`
}
```

Add `Update UpdateSettings json:"update"` to `State`, add `DefaultUpdateSettings()`, and call it from `DefaultState()` and normalization.

- [ ] **Step 4: Verify GREEN**

Run: `./.tools/go/bin/go test ./internal/config`

Expected: PASS.

## Task 2: Update Package Core

**Files:**
- Create: `internal/update/github.go`
- Create: `internal/update/bundle.go`
- Create: `internal/update/archive.go`
- Create: `internal/update/update_test.go`

- [ ] **Step 1: Write failing tests**

Add tests for:

```go
func TestSelectAssetMatchesPattern(t *testing.T)
func TestExtractTarGzRejectsPathTraversal(t *testing.T)
func TestValidateBundleAcceptsLinuxARM64Bundle(t *testing.T)
func TestValidateBundleRejectsWrongPlatform(t *testing.T)
```

Tests create temporary bundle trees with `vpn-manager`, `start.sh`, `bundle-info.txt`, `bin/openvpn`, and `bin/sing-box`.

- [ ] **Step 2: Verify RED**

Run: `./.tools/go/bin/go test ./internal/update`

Expected: compile failure because package functions are not defined.

- [ ] **Step 3: Implement minimal helpers**

Implement:

```go
func SelectAsset(release GitHubRelease, pattern string) (GitHubAsset, error)
func ExtractTarGz(archivePath string, targetDir string) error
func ParseBundleInfo(path string) (BundleInfo, error)
func ValidateBundle(root string) (BundleInfo, error)
```

Extraction must reject absolute paths, `..`, non-regular files except directories, and unsafe symlinks.

- [ ] **Step 4: Verify GREEN**

Run: `./.tools/go/bin/go test ./internal/update`

Expected: PASS.

## Task 3: Installer And Operation Lock

**Files:**
- Create: `internal/update/manager.go`
- Create: `internal/update/install.go`
- Modify: `internal/update/update_test.go`

- [ ] **Step 1: Write failing tests**

Add tests:

```go
func TestInstallBundlePreservesDataAndCreatesBackup(t *testing.T)
func TestManagerRejectsConcurrentOperation(t *testing.T)
```

The install test creates an app dir with old `vpn-manager`, `start.sh`, `bin/openvpn`, `bin/sing-box`, and `data/vpn-manager.db`; installs a new extracted bundle; asserts data remains and backup has old runtime files.

- [ ] **Step 2: Verify RED**

Run: `./.tools/go/bin/go test ./internal/update -run 'TestInstallBundle|TestManagerRejects'`

Expected: compile failure or failing assertions because installer/manager do not exist.

- [ ] **Step 3: Implement installer**

Implement `Manager` with:

```go
type Manager struct {
	appDir string
	dataDir string
	state *config.Manager
	recordEvent func(level, kind, message string)
	mu sync.Mutex
	operation *Operation
	httpClient *http.Client
	restart func()
}
```

Implement install replacement with backup under `backups/update-YYYYMMDD-HHMMSS`, skip `data`, skip `backups`, and set executable bits.

- [ ] **Step 4: Verify GREEN**

Run: `./.tools/go/bin/go test ./internal/update`

Expected: PASS.

## Task 4: API Wiring

**Files:**
- Modify: `internal/api/handler.go`
- Modify: `internal/api/handler_test.go`
- Modify: `cmd/vpn-manager/main.go`

- [ ] **Step 1: Write failing API tests**

Add tests for:

```go
func TestUpdateStatusEndpoint(t *testing.T)
func TestUpdateUploadRejectsNonLinux(t *testing.T)
```

Use an update manager test double or real manager with restart disabled.

- [ ] **Step 2: Verify RED**

Run: `./.tools/go/bin/go test ./internal/api -run TestUpdate`

Expected: 404 or compile failure before routes exist.

- [ ] **Step 3: Add routes**

Wire:

```go
mux.HandleFunc("/api/system/update", handler.handleSystemUpdate)
mux.HandleFunc("/api/system/update/settings", handler.handleSystemUpdateSettings)
mux.HandleFunc("/api/system/update/check", handler.handleSystemUpdateCheck)
mux.HandleFunc("/api/system/update/install", handler.handleSystemUpdateInstall)
mux.HandleFunc("/api/system/update/upload", handler.handleSystemUpdateUpload)
```

Add `Update *update.Manager` to `api.Dependencies` and `Handler`.

- [ ] **Step 4: Verify GREEN**

Run: `./.tools/go/bin/go test ./internal/api -run TestUpdate`

Expected: PASS.

## Task 5: Settings UI

**Files:**
- Modify: `frontend/src/pages/SettingsPage.jsx`
- Modify: `frontend/src/i18n.jsx`

- [ ] **Step 1: Add UI state and actions**

Add `updateStatus`, `updateError`, `updateMessage`, `checkingUpdate`, `installingUpdate`, and `uploadingUpdate`. Add handlers for status load, check, install, and file upload.

- [ ] **Step 2: Add section**

Add a Settings section with icon `system_update_alt`, repo/pattern inputs, check/install buttons, upload input, latest asset summary, and notices.

- [ ] **Step 3: Add translations**

Add Russian and English keys for labels, buttons, loading states, and errors.

- [ ] **Step 4: Verify frontend build**

Run: `npm --prefix frontend run build`

Expected: Vite build succeeds.

## Task 6: Full Verification

**Files:**
- All changed files.

- [ ] **Step 1: Run Go tests**

Run: `./.tools/go/bin/go test ./...`

Expected: PASS.

- [ ] **Step 2: Run router package build**

Run: `bash ./package_router.sh`

Expected: bundle builds successfully and `find build/router -maxdepth 2 -type f \( -name openvpn -o -name sing-box \) -print` lists only `build/router/bin/openvpn` and `build/router/bin/sing-box`.

- [ ] **Step 3: Inspect worktree**

Run: `git status --short`

Expected: changes are limited to existing VPN/package edits plus update feature docs/backend/frontend generated bundle.
