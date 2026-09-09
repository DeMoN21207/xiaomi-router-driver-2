# VPN rollback and responsive status — 2026-09-09

## Changes

Subscription application now saves the previous outbound configuration, exact
routing settings, domain/IP entries and an ipset snapshot before changing a
runtime. If replacement startup, interface readiness, routing or persistence
fails, it cleans up the changed runtimes and restores their previous plans.
All cleanup runs before restoration, since different location keys can share
routing table numbers. Untouched VPNs keep their existing processes and routes.

Rollback has its own bounded context, independent of a cancelled client request.
Already cancelled operations are rejected before stopping a running VPN. A new
process exiting before its interface appears is detected without waiting the
entire interface timeout. Automatic selection thresholds remain 15/60 seconds.

Active `.domains.list` files are now retained alongside their configs and kept
by runtime pruning, so rollback does not reconstruct old rules from edited UI
settings. Legacy domain-only routes can migrate from their dnsmasq fragments.
One legacy runtime can also use the previous applied global entries, with count
and fragment membership checks. If a running legacy multi-runtime setup lacks
exact entries, the operation fails before mutation instead of guessing them.

Runtime snapshots, priority status and provider failover status no longer wait
for slow route application. Priority evaluation remains serialized separately
from status reads and override writes. A manual application loads the latest
configuration/runtime after obtaining that serialization lock; successful
selection and completion timestamps are published only after application.

## Validation

- Regression tests first reproduced lost previous runtime and blocked status.
- Tests cover replacement routing failure, process exit, caller cancellation,
  table-index changes, partial teardown without harming an untouched runtime,
  rejected already-cancelled requests, retained sidecars and legacy migration.
- Priority/provider status tests hold probes and applies behind channels and
  verify responsive reads, failed-apply state, concurrent manual overrides and
  fresh snapshots for queued manual applications.
- Nine isolated subscription scenarios passed on the router's Linux ARM64.
  These use temporary child processes and a synthetic routing script, not the
  live routing tables. A fixture was adjusted to the firmware's integer-only
  `sleep` implementation before the complete native run passed.
- IPset data is captured once per previous runtime and reused for eligible fast
  failover, avoiding a second `ipset save` subprocess.
- Final `go test -race ./...`: all 15 test packages PASS. `go vet ./...`,
  `git diff --check`, Linux ARM64 build and installer shell syntax: PASS.
- Independent code review found and verified fixes for partial teardown table
  ownership, cancellation before mutation and the empty desired-set path.

## Deployment and live checks

Deployed via SSH with a controlled restart of the manager's verified VPN child.
The previous binary, bundle metadata and configuration were backed up to
`/mnt/usb-4d3e56cb/vpn-manager/.deploy-backups/transactional-recovery-20260909`.

- Build: `31f92b9-dirty`, `2026-09-09T20:07:59Z`, Linux ARM64.
- Installed and local binary MD5: `1f08fdf578431869a6db8f0ed6e78943`.
- Manager PID 18974 owns sing-box PID 19330; table 101 routes through
  `sbd9843049ba`. The active sidecar contains all 159 routing entries, and the
  provider source cache remains present.
- At `20:10:30Z`, Latvia #2 had 12 consecutive successful checks and zero
  failures. HTTPS inside the tunnel succeeded in 220 ms; provider health was
  healthy, with no recent warning/error events.
- Six policy targets and the 15-second failure / 60-second restoration settings
  remained configured.
- A 35-second sample across deployment recorded 33 successful responses for
  each status endpoint. Maximum response times: `/api/status` 294.53 ms,
  `/api/priority-policies/status` 302.92 ms, `/api/failover/status` 282.17 ms.
  Each endpoint had one connection error during the service restart; none
  reached the two-second request timeout. Measurements are in the ignored local
  `build/transactional-deploy-status-latency.json`.

This is recovery from application/command failures while the manager is running;
it is not a persistent transaction journal for power loss. No throughput change
is claimed from these reliability changes.
