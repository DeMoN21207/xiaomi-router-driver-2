# Router Resilience Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** Make VPN Manager survive interrupted updates and process failures, keep failover responsive, protect dangerous API operations, and reduce database, DNS, and polling failure modes.

**Architecture:** Introduce durable update and database maintenance primitives, one coordinated application lifecycle, explicit background apply operations, and narrowly scoped health/auth middleware. Preserve the existing managers and data model where their behavior is already safe.

**Tech Stack:** Go 1.25, `net/http`, `database/sql`, modernc SQLite, OpenWrt procd/cron shell, React 19, Vite 7.

**Spec:** `docs/superpowers/specs/2026-09-10-router-resilience-hardening-design.md`

## Global Constraints

- Target Linux ARM64 and Xiaomi/OpenWrt with a USB-backed application directory.
- Keep `data/` outside update replacement and preserve existing configurations.
- Keep CGO disabled and SQLite limited to one open connection.
- Do not replace a complete configuration snapshot during rollback.
- Keep public read-only routes compatible; protect dangerous mutations when an admin token exists.
- Develop every behavior change with a failing test before production code.

---

### Task 1: Durable atomic runtime update

**Files:**
- Create: `internal/update/transaction.go`
- Modify: `internal/update/install.go`
- Modify: `internal/update/manager.go`
- Test: `internal/update/update_test.go`
- Test: `internal/update/recovery_test.go`

**Interfaces:**
- Produces: `RecoverInterruptedUpdate(appDir string) error`, durable `.update-journal.json`, and staged same-filesystem atomic replacement.
- Consumes: existing `ValidateBundle`, `backupRuntime`, and `copyPath` helpers.

- [x] Write tests that interrupt staging, interrupt replacement, and verify startup recovery restores executable runtime files while preserving `data/`.
- [x] Run `go test ./internal/update -run 'Atomic|Interrupted|Recover' -count=1` and confirm failures identify missing transaction behavior.
- [x] Implement staging copies with file and directory sync, phase journal writes through temp-file rename, atomic target swaps, and backup restoration.
- [x] Re-run the focused tests and `go test ./internal/update -count=1`.
- [x] Commit update transaction and recovery behavior.

### Task 2: Coordinated shutdown and restart

**Files:**
- Create: `internal/lifecycle/coordinator.go`
- Test: `internal/lifecycle/coordinator_test.go`
- Modify: `cmd/vpn-manager/main.go`
- Modify: `internal/update/manager.go`

**Interfaces:**
- Produces: `Coordinator.Run(ctx, server, shutdown, restart) error` and an update restart request carrying backup recovery metadata.
- Consumes: updater restart callback and all worker `Run(context.Context)` methods.

- [x] Write tests proving SIGTERM-style cancellation calls HTTP shutdown, worker cancellation, SQLite checkpoint/close, and restart only after cleanup.
- [x] Verify the lifecycle tests fail before implementation.
- [x] Implement `signal.NotifyContext`, a shared root context, graceful HTTP shutdown, ordered cleanup, and Linux `syscall.Exec` restart with backup restoration on failure.
- [x] Run lifecycle tests and `go test ./cmd/vpn-manager ./internal/lifecycle ./internal/update -count=1`.
- [x] Commit coordinated lifecycle behavior.

### Task 3: Single-owner watchdog

**Files:**
- Modify: `internal/automation/manager.go`
- Test: `internal/automation/manager_test.go`
- Modify: `packaging/router/start.sh`
- Modify: `deploy_router.sh`

**Interfaces:**
- Produces: generated watchdog script using PID plus `/proc/<pid>/exe`, update journal awareness, and unlimited procd respawn.
- Consumes: update journal phase and existing `/tmp/vpn-manager.pid` convention.

- [x] Add failing render tests for stale PID, mismatched executable, active update journal, and `respawn 3600 5 0`.
- [x] Implement the generated shell checks and deployment lock handling.
- [x] Run automation tests and `sh -n packaging/router/start.sh deploy_router.sh`.
- [x] Commit watchdog ownership changes.

### Task 4: WAN evidence and ungated local recovery

**Files:**
- Create: `internal/status/wan_probe.go`
- Modify: `internal/status/service.go`
- Test: `internal/status/wan_probe_test.go`
- Modify: `internal/automation/supervisor.go`
- Test: `internal/automation/supervisor_test.go`

**Interfaces:**
- Produces: `WANProbeResult` using detected/overridden interface and multiple targets.
- Consumes: runtime snapshot and existing provider probe functions.

- [x] Add failing tests for one failed target, missing default route, interface binding, and recovery of a missing runtime while WAN is down.
- [x] Implement interface/default-route detection and multi-target probing with environment overrides.
- [x] Separate local runtime reconciliation from provider switching WAN gates.
- [x] Run status and automation test packages.
- [x] Commit WAN and recovery changes.

### Task 5: SQLite maintenance and recovery

**Files:**
- Create: `internal/sqlitedb/maintenance.go`
- Test: `internal/sqlitedb/maintenance_test.go`
- Modify: `internal/sqlitedb/sqlitedb.go`
- Modify: `cmd/vpn-manager/main.go`

**Interfaces:**
- Produces: `OpenWithRecovery(path)`, `Checkpoint(mode)`, `Backup(path)`, and a cancellable periodic maintenance worker.
- Consumes: existing single-connection SQLite configuration.

- [x] Add failing tests for quick-check rejection, corrupt-file preservation, backup restore, atomic backup, and checkpoint execution.
- [x] Implement a 15-second busy timeout, quick-check startup, `VACUUM INTO` backup, passive periodic checkpoint, truncate shutdown checkpoint, and last-good restoration.
- [x] Run SQLite tests including forced corruption and the full config/events/status packages.
- [x] Commit SQLite resilience.

### Task 6: Async apply and precise rollback

**Files:**
- Create: `internal/api/operations.go`
- Test: `internal/api/operations_test.go`
- Modify: `internal/api/handler.go`
- Modify: `internal/config/state.go`
- Test: `internal/api/handler_apply_test.go`
- Test: `internal/config/state_test.go`
- Modify: `frontend/src/api.js`
- Modify: `frontend/src/pages/ConnectionsPage.jsx`

**Interfaces:**
- Produces: `POST /api/rules/apply -> 202 {operation}`, `GET /api/rules/apply/{id}`, and conditional per-entity restore methods.
- Consumes: `applyMu`, `UpdateRule`, `DeleteRule`, and current apply status UI.

- [x] Add failing tests for operation transitions and a concurrent edit surviving failed apply rollback.
- [x] Implement the operation registry and optimistic targeted restore without `Save(previousState)`.
- [x] Add frontend polling with a five-minute operation deadline and progress messaging.
- [x] Run API/config tests, frontend tests, and build.
- [x] Commit async apply and precise rollback.

### Task 7: HTTP limits and dangerous-route authorization

**Files:**
- Create: `internal/api/auth.go`
- Test: `internal/api/auth_test.go`
- Modify: `internal/api/handler.go`
- Modify: `cmd/vpn-manager/main.go`
- Modify: `frontend/src/api.js`
- - Modify: `frontend/src/api.js`

**Interfaces:**
- Produces: optional bearer-token middleware, 128 MiB upload cap, five-minute body deadline, and a frontend 401 token flow using `sessionStorage`.
- Consumes: `VPN_MANAGER_API_TOKEN` and `data/api-token`.

- [x] Add failing API tests for read-only access, rejected dangerous mutation, accepted bearer token, and oversized upload.
- [x] Implement token loading, route classification, constant-time comparison, upload limit, and server deadlines.
- [x] Implement one-time token prompt and automatic retry of the rejected request.
- [x] Run API and frontend tests/build.
- [x] Commit API protection and limits.

### Task 8: Bounded DNS proxy with health

**Files:**
- Modify: `internal/dnsproxy/server.go`
- Test: `internal/dnsproxy/server_test.go`
- Modify: `internal/status/service.go`
- Modify: `cmd/vpn-manager/main.go`

**Interfaces:**
- Produces: bounded concurrent resolver, direct Do53 fallbacks, circuit state, and `DNSProxyHealth` in status.
- Consumes: existing DoH resolver and DNS proxy configuration environment variables.

- [x] Add failing tests for concurrency rejection, DoH fallback, loop prevention, recovery, and health counters.
- [x] Implement a semaphore, configurable Do53 endpoints, failure threshold/cooldown, and health snapshot.
- [x] Expose health through `/api/status`.
- [x] Run DNS and status tests.
- [x] Commit DNS resilience.

### Task 9: Polling, conntrack, defaults, logging, and doctor

**Files:**
- Modify: `frontend/src/dashboardPreferences.js`
- Modify: `frontend/src/resourceLoader.js`
- Test: `frontend/src/resourceLoader.test.js`
- Modify: `internal/routing/update_routes.sh`
- Test: `internal/routing/apply_test.go`
- Modify: `internal/config/state.go`
- Modify: `internal/automation/manager.go`
- Modify: `cmd/vpn-manager/main.go`
- Modify: `internal/doctor/doctor.go`
- Test: `internal/doctor/doctor_test.go`

**Interfaces:**
- Produces: polling floor of five seconds, endpoint-specific deadlines, destination-scoped conntrack cleanup without a global table flush, router-first-install automation defaults, selective request logging, and expanded diagnostics.
- Consumes: current load profiles, generated watchdog, status and runtime databases.

- [x] Add failing frontend tests for invalid sub-five-second preferences and longer history deadlines.
- [x] Add failing Go/shell tests for destination-scoped cleanup, first-install defaults, log filtering, and new doctor checks.
- [x] Implement the minimal production changes and expanded read-only doctor reporting.
- [x] Run focused frontend, routing, automation, and doctor tests.
- [x] Commit operational hardening.

### Task 10: Full verification, package, deploy, and publish

**Files:**
- Modify: `docs/2026-09-10-router-resilience-hardening.md`
- Modify generated frontend assets and `build/router/*` through standard scripts.

**Interfaces:**
- Produces: tested ARM64 router bundle and documented rollback point.
- Consumes: all previous tasks.

- [x] Run `go test -race ./...`, `go vet ./...`, frontend tests/build, shell syntax checks, and `git diff --check`.
- [x] Build with `package_router.sh` and verify bundle metadata and executable architecture.
- [x] Create a router backup and deploy with `deploy_router.sh`.
- [x] Verify process uniqueness, database check, WAN state, DNS health, apply operation, protected endpoints, traffic UI, and automatic recovery on the live router.
- [x] Merge the feature branch into `main`, regenerate the committed bundle from the clean source commit, push `origin/main`, and verify local/remote/router commit identities.
