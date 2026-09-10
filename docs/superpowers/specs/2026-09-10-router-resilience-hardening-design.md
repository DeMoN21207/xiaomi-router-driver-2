# Router Resilience Hardening Design

## Goal

Keep VPN Manager recoverable during interrupted updates, process crashes, WAN probe failures, slow API operations, concurrent edits, SQLite faults, and DNS upstream failures while preserving the current router data directory and routing behavior.

## Constraints

- Target Linux ARM64 and Xiaomi/OpenWrt with a USB-backed application directory.
- Keep `data/` outside update replacement and preserve existing configuration.
- Keep the CGO-free `modernc.org/sqlite` driver and one database connection.
- Existing configurations remain valid without migration input from the user.
- Public read-only API routes stay available on LAN. Dangerous mutations require an admin token when one is configured.
- Route application remains transactional from the user's perspective and never restores a stale complete configuration snapshot.

## Update and process lifecycle

An update is extracted and validated before runtime mutation. Every file is copied into a same-filesystem staging tree, flushed with `fsync`, and validated for presence and executable mode. A durable journal records the backup directory, staged directory, and phase. Installation replaces each runtime entry with atomic rename operations. An interrupted install is detected on the next start and restored from its recorded backup before normal initialization.

The update endpoint schedules a coordinated restart after its response is written. A root context controls the HTTP server, automation supervisor, traffic samplers, and DNS proxy. Shutdown stops accepting HTTP requests, cancels workers, checkpoints SQLite, closes the database, and then replaces the current process with the new executable through `syscall.Exec`. If exec fails, the updater restores the backup and executes the old binary.

The update journal doubles as an `updating.lock`. Both generated watchdogs skip startup while an update is in a mutating or restarting phase. The cron watchdog validates the PID file and `/proc/<pid>/exe`; procd uses unlimited respawn only after its startup wrapper has validated or recovered the runtime.

## WAN and failover

WAN status is derived from link/default-route evidence and multiple external probes bound to the detected WAN interface. `VPN_MANAGER_WAN_IFACE` overrides detection and `VPN_MANAGER_WAN_PROBES` overrides the target list. One failed ICMP target cannot mark WAN down.

Provider switching still requires usable WAN. Recovery of a missing local VPN process does not: the supervisor may reconcile a stopped runtime while WAN is down or unknown. This separates local process recovery from internet reachability.

## SQLite durability

SQLite remains single-connection WAL. Startup runs `quick_check`; corruption moves the database and sidecars to a timestamped `.corrupt-*` set and restores the last known-good backup. A scheduled passive checkpoint bounds WAL growth. Graceful shutdown runs a truncate checkpoint. Backups use SQLite `VACUUM INTO` under the database maintenance lock and are atomically renamed.

## Apply and configuration consistency

Long route applications are represented by an in-memory operation registry. `POST /api/rules/apply` returns `202` with an operation ID and `GET /api/operations/{id}` exposes queued/running/succeeded/failed status. The frontend polls this endpoint and shows progress.

Configuration has a monotonically increasing generation. Mutations return the new generation. A failed rule apply conditionally restores only the affected rule when its generation is still current. Concurrent edits remain intact. Runtime rollback uses the captured prior runtime state without saving that entire state back into the configuration store.

The HTTP server keeps a five-second header deadline, allows five minutes for request bodies and responses, and limits update uploads to 128 MiB.

## API authorization

An optional admin token is read from `VPN_MANAGER_ADMIN_TOKEN` or `data/admin-token`. When present, reboot, update, apply, configuration mutations, and automation mutations require `Authorization: Bearer <token>`. The SPA keeps the token in `sessionStorage`, prompts on a 401 response, and never writes it into the configuration API or logs. Existing installations without a token retain current access until the administrator enables protection.

## DNS, polling, logging, and diagnostics

The DNS proxy caps concurrent queries, reports health counters, and uses configurable direct Do53 fallbacks after all DoH upstreams fail. It never forwards fallback traffic to its own dnsmasq listener. DNS health is included in `/api/status`.

Dashboard polling offers 5, 10, 30, and 60 seconds, with 10 seconds as default. Expensive history resources use a longer deadline than status resources. Conntrack cleanup remains enabled for failover but receives the changed destination set so unrelated routed sessions are preserved.

Request logging records mutations, slow requests, and 4xx/5xx responses; routine successful polling is omitted. Runtime event logs remain warn/error only. Router bundle bootstrap enables service installation and automatic recovery with a 15-second failure threshold on first install without overriding existing settings.

Doctor adds executable validation, SQLite quick check and WAL size, disk space and inodes, PID uniqueness and executable identity, recorded sing-box PID validation, and an optional HTTPS-through-tunnel probe. Scheduled diagnostics record only failures.

## Verification and rollout

Each subsystem is developed test-first. Verification includes Go unit and race tests, vet, frontend tests and build, shell syntax, Linux ARM64 packaging, installer interruption simulations, concurrent configuration tests, and live API/UI smoke tests. Deployment preserves a pre-change router backup. The release is pushed to `main` only after the router reports healthy runtime, applied routes, clean database checks, and the expected bundle commit.
