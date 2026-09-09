# Additional priority recovery fixes — 2026-09-09

## Fixed and deployed

Two regressions in `internal/automation/priority.go` shared the same cause: the
automatic restoration delay also applied to explicit manual selections and to
recovery from an already failed backup.

- Selecting a healthy server manually now takes effect after its health check.
  Previously the 60-second restoration gate rejected the selection, and the
  override was then removed. An unhealthy manual target is still rejected.
- When the active backup has failed for the configured 15-second grace period
  and required failure streak, a working primary can be selected immediately.
  Previously it could remain on the failed backup until the separate 60-second
  restoration gate elapsed.
- Automatic return from a working backup still requires the restoration grace
  period and success streak. The change only affects the selection decision;
  probe scheduling and tunnel startup still add time to actual recovery.

## Verification

- New regression tests in `internal/automation/priority_recovery_test.go` failed
  before the fix and passed afterward.
- `go test -race ./internal/automation -count=1`: PASS.
- `go vet ./internal/automation`, `git diff --check`: PASS.
- Linux ARM64 application build: PASS. Independent focused code review found
  no issues; existing automatic restoration tests still pass.
- Deployment via SSH; local and router binary MD5 both
  `6017f02b97c7344dd24ba9eca2eda46c`.
- Bundle metadata: `31f92b9-dirty`, built at `2026-09-09T19:37:14Z`.
- Backup of the previous working binary, metadata and configuration:
  `/mnt/usb-4d3e56cb/vpn-manager/.deploy-backups/priority-recovery-20260909`.
- At `19:38:20Z`, Latvia #2 had six consecutive successful checks, zero failures,
  and HTTPS through `sbd9843049ba` succeeded in 199 ms. WAN was up, the application
  reported no last error, and table 101 routed through the active tunnel.
- All six policy targets and the 15/60-second settings remained configured.
  No intentional live outage or throughput benchmark was performed.

## Remaining findings

1. Subscription application stops obsolete instances before starting and
   validating replacements. Failure during replacement startup or routing calls
   cleanup, without restoring the previous working runtime. A transaction with
   a retained rollback plan would improve recovery from local startup failures.
2. Priority evaluation holds `priorityMu` while applying routes. During this
   deployment, `/api/priority-policies/status` exceeded a 10-second client timeout
   while startup was applying routes. Publishing a separate status snapshot would
   keep the panel responsive during these operations.
3. System swap was full, but this did not indicate swapping by these processes:
   before deployment, manager RSS was 23,460 kB and sing-box RSS was 22,584 kB,
   both with zero process swap. Available system RAM was about 156 MB. Kernel
   unreclaimable slab was about 281 MB; this measurement alone does not establish
   a leak or justify changing kernel settings.

This deployment supersedes the application build recorded in
`2026-09-09-fast-failover.md`; its server measurements and routing changes remain
applicable.
