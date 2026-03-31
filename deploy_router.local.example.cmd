@echo off
rem Copy this file to deploy_router.local.cmd and fill in the values below.

set "ROUTER_HOST=192.168.31.1"
set "ROUTER_PORT=22"
set "ROUTER_USER=root"
set "ROUTER_PASSWORD=replace-with-router-password"
set "ROUTER_REMOTE_DIR=/mnt/usb-4d3e56cb/vpn-manager"
set "ROUTER_SERVICE=vpn-manager"
set "ROUTER_HTTP_PORT=18080"
set "ROUTER_HOSTKEY=ssh-rsa 2048 SHA256:replace-with-current-router-hostkey"
