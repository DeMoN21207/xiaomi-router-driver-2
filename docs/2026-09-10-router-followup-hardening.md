# Router follow-up hardening plan

## Goal

Close the remaining runtime and configuration races found after the first resilience rollout without interrupting the working VPN configuration.

## Work

1. Preserve policy routes when an owned OpenVPN or sing-box process exits unexpectedly. The supervisor will observe the stopped runtime and replace it; explicit cleanup still removes routes.
2. Add one identity-checked helper for killing persisted PIDs and use it for OpenVPN and subscription cleanup after an application restart.
3. Thread request/apply contexts through subscription downloads and retries so cancellation stops the active HTTP request and prevents later retry profiles.
4. Make provider and automation mutations atomic inside `config.Manager`, including conditional automation rollback after service sync failure.
5. Implement the configured `direct` all-down failover mode and retain `keep` as the default.
6. Shorten lock hold times around failover status where practical, align the dashboard history deadline with the shared request deadline, and verify the remaining operational notes.
7. Run Go tests with the race detector, vet, frontend tests/build, and shell syntax checks. Package and deploy only after those checks pass; keep the live pre-deploy backup and verify API, traffic, DNS, WAN, process count, and recovery stability before pushing `main`.

## Safety checks

- Do not change the live provider, rule, priority target, or routing settings during deployment.
- Keep routes on unexpected process exit; remove them only during an explicit serialized cleanup/apply.
- Refuse to kill a persisted PID unless its current `/proc/<pid>/cmdline` matches the expected binary and runtime-specific marker.
- Keep the current router package available through the deploy script's pre-deploy backup.
