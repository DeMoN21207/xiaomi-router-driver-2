# Router Bundle

This directory is the ready-to-copy runtime bundle for the router.

Build entrypoints:
- `build.bat`
- `build.sh`
- `package_router.bat`
- `package_router.sh`

Release archive:
- `package_router.sh` and `package_router.bat` also create `build/vpn-manager-linux-arm64.tar.gz` for GitHub Releases.

Files:
- `vpn-manager` is the Linux router binary.
- `bin/openvpn` and `bin/sing-box` are bundled runtime helper binaries.
- `start.sh` starts the app from this directory.
- `data/` is the runtime data directory. The app fills it on first start.
- `bundle-info.txt` contains the bundle build settings.

Copy:
1. Copy the whole directory to the router.
2. Place it in the final target directory, for example `/mnt/usb-XXXX/vpn-manager`.

Start:
1. `cd /path/to/vpn-manager`
2. `chmod +x vpn-manager start.sh bin/openvpn bin/sing-box 2>/dev/null || true`
3. `VPN_MANAGER_PORT=18080 ./start.sh`

What the app creates on first start:
- `data/vpn-manager.db`
- `data/vpn-state.json`
- `data/events.json`
- `data/.vpn-manager/update_routes.sh`

Requirements:
- The router architecture must match the built binary in `bundle-info.txt`.
- `openvpn` and `sing-box` must be available in `PATH`, `./bin/`, or `./.vpn-manager/bin/`.
- Root permissions are required only if you want to enable system autostart/service install.
