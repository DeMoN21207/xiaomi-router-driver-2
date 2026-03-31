# xiaomi-router-driver-2

Linux-first router bundle for running `vpn-manager` on OpenWrt/Xiaomi routers.

## What is in the repo

- Go backend in `cmd/` and `internal/`
- React frontend in `frontend/`
- Router bundle templates in `packaging/router/`
- Linux bundle build scripts in `build.*` and `package_router.*`
- Ready-to-copy router bundle in `build/router`

## Build

The primary build target is the router bundle in `build/router`.

- Windows: `build.bat`
- Linux/macOS shell: `./build.sh`

## Deploy

The repository keeps the ready router bundle in `build/router`.

Local deployment credentials are intentionally not versioned.
Use `deploy_router.local.example.cmd` as a template for a local ignored `deploy_router.local.cmd`.
Copy the contents of `build/router` to the router and start the service there.

## Runtime binaries

The application expects Linux `openvpn` and `sing-box` binaries on the router.

They can be provided in one of these ways:

- bundled in `build/router/bin`
- passed explicitly through `ROUTER_OPENVPN_BIN` and `ROUTER_SINGBOX_BIN`
- fetched from the target router during deploy if they are missing locally
