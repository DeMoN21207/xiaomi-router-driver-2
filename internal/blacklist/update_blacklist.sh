#!/bin/sh
# Blacklist manager: block IPs via iptables DROP, block domains via dnsmasq sinkhole.
# Usage: update_blacklist.sh <add|del>
#
# Environment variables:
#   BLACKLIST_DOMAINS_FILE  - path to file with blocked domains (one per line)
#   BLACKLIST_IPS_FILE      - path to file with blocked IPs/CIDRs (one per line)
#   BLACKLIST_DNSMASQ_FILE  - dnsmasq config path (default: /tmp/dnsmasq.d/blacklist_dns.conf)
#   LAN_IFACE               - LAN interface (default: br-lan)

set -e

ACTION="${1:-add}"
BLACKLIST_DOMAINS_FILE="${BLACKLIST_DOMAINS_FILE:-blacklist_domains.list}"
BLACKLIST_IPS_FILE="${BLACKLIST_IPS_FILE:-blacklist_ips.list}"
BLACKLIST_DNSMASQ_FILE="${BLACKLIST_DNSMASQ_FILE:-/tmp/dnsmasq.d/blacklist_dns.conf}"
LAN_IFACE="${LAN_IFACE:-br-lan}"
CHAIN_NAME="BLACKLIST_BLOCK"

cleanup_iptables() {
    # Remove jump rule from FORWARD if it exists.
    while iptables -D FORWARD -i "$LAN_IFACE" -j "$CHAIN_NAME" 2>/dev/null; do :; done
    # Flush and delete chain if it exists.
    iptables -F "$CHAIN_NAME" 2>/dev/null || true
    iptables -X "$CHAIN_NAME" 2>/dev/null || true
}

apply_iptables() {
    cleanup_iptables

    if [ ! -f "$BLACKLIST_IPS_FILE" ] || [ ! -s "$BLACKLIST_IPS_FILE" ]; then
        return
    fi

    iptables -N "$CHAIN_NAME"

    while IFS= read -r ip; do
        ip=$(echo "$ip" | tr -d '[:space:]')
        [ -z "$ip" ] && continue
        case "$ip" in \#*) continue ;; esac
        iptables -A "$CHAIN_NAME" -d "$ip" -j DROP
    done < "$BLACKLIST_IPS_FILE"

    iptables -I FORWARD 1 -i "$LAN_IFACE" -j "$CHAIN_NAME"
}

apply_dnsmasq() {
    mkdir -p "$(dirname "$BLACKLIST_DNSMASQ_FILE")"

    if [ ! -f "$BLACKLIST_DOMAINS_FILE" ] || [ ! -s "$BLACKLIST_DOMAINS_FILE" ]; then
        rm -f "$BLACKLIST_DNSMASQ_FILE"
        return
    fi

    : > "$BLACKLIST_DNSMASQ_FILE"
    while IFS= read -r domain; do
        domain=$(echo "$domain" | tr -d '[:space:]')
        [ -z "$domain" ] && continue
        case "$domain" in \#*) continue ;; esac
        echo "address=/${domain}/0.0.0.0" >> "$BLACKLIST_DNSMASQ_FILE"
    done < "$BLACKLIST_DOMAINS_FILE"
}

restart_dnsmasq() {
    if [ -x /etc/init.d/dnsmasq ]; then
        /etc/init.d/dnsmasq restart 2>/dev/null || true
    fi
}

case "$ACTION" in
    add)
        apply_iptables
        apply_dnsmasq
        restart_dnsmasq
        ;;
    del)
        cleanup_iptables
        rm -f "$BLACKLIST_DNSMASQ_FILE"
        restart_dnsmasq
        ;;
    *)
        echo "Usage: $0 <add|del>" >&2
        exit 1
        ;;
esac
