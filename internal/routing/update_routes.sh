#!/bin/sh

# --- SETTINGS ---
DOMAIN_LIST="${DOMAIN_LIST:-domains.list}"
VPN_GATEWAY="${VPN_GATEWAY:-10.8.0.1}"
VPN_ROUTE_MODE="${VPN_ROUTE_MODE:-gateway}"
VPN_MASQUERADE="${VPN_MASQUERADE:-1}"

# --- IMPORTANT INTERFACE SETTINGS ---
# Run `ip a` on your router to see interface names.
LAN_IFACE="${LAN_IFACE:-br-lan}"      # Your router's LAN interface (e.g. br-lan, br0, eth1)
VPN_IFACE="${VPN_IFACE:-tun0}"        # Your VPN client's tunnel interface (e.g. tun0)

# --- Advanced settings ---
TABLE_NUM="${TABLE_NUM:-101}"
FW_ZONE_CHAIN="${FW_ZONE_CHAIN:-zone_lan_forward}" # The firewall chain for the LAN zone
IPSET_NAME="${IPSET_NAME:-vpn_hosts}"
FWMARK="${FWMARK:-0x1}"
DNSMASQ_CONFIG_FILE="${DNSMASQ_CONFIG_FILE:-/tmp/dnsmasq.d/vpn_dns.conf}"
DOMAIN_STATS_CHAIN="${DOMAIN_STATS_CHAIN:-VDS_${IPSET_NAME}}"
LEGACY_DOMAIN_STATS_CHAIN="${LEGACY_DOMAIN_STATS_CHAIN:-VPN_DOM_STATS_${IPSET_NAME}}"
# --- END OF SETTINGS ---

short_hash() {
    value="$1"
    hash=$(printf '%s' "$value" | md5sum 2>/dev/null | cut -c1-8)
    if [ -z "$hash" ]; then
        hash=$(printf '%s' "$value" | cksum | cut -d' ' -f1 | cut -c1-8)
    fi
    printf '%s' "$hash"
}

resolve_domain_ips() {
    domain="$1"
    nslookup "$domain" 127.0.0.1 2>/dev/null \
        | awk '/^Name:/ {capture=1; next} capture && /^Address [0-9]+: / {print $3} capture && /^Address: / {print $2}' \
        | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' \
        | sort -u
}

# --- SCRIPT LOGIC ---
# Check for required utilities
for cmd in ip iptables ipset tr; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "Error: required utility '$cmd' not found."
        exit 1
    fi
done

# Cleanup old rules before adding new ones
cleanup_firewall() {
    # Mangle and IP rule cleanup
    iptables -t mangle -D PREROUTING -i "$LAN_IFACE" -m set --match-set "$IPSET_NAME" dst -j MARK --set-mark "$FWMARK" >/dev/null 2>&1
    ip rule del fwmark "$FWMARK" table "$TABLE_NUM" >/dev/null 2>&1

    # Forwarding and NAT cleanup
    iptables -D "$FW_ZONE_CHAIN" -i "$LAN_IFACE" -o "$VPN_IFACE" -j ACCEPT >/dev/null 2>&1
    iptables -t nat -D POSTROUTING -o "$VPN_IFACE" -j MASQUERADE >/dev/null 2>&1

    # Domain stats accounting chain cleanup
    for stats_chain in "$DOMAIN_STATS_CHAIN" "$LEGACY_DOMAIN_STATS_CHAIN"; do
        if [ -z "$stats_chain" ]; then continue; fi
        iptables -D FORWARD -o "$VPN_IFACE" -j "$stats_chain" >/dev/null 2>&1
        iptables -F "$stats_chain" >/dev/null 2>&1
        iptables -X "$stats_chain" >/dev/null 2>&1
    done

    legacy_domain_prefix="vpn_d_${IPSET_NAME}_"
    compact_domain_prefix="vd_$(short_hash "$IPSET_NAME")_"

    # Destroy per-domain ipsets (legacy and compact prefixes)
    for set_name in $(ipset list -n 2>/dev/null | grep -E "^(${legacy_domain_prefix}|${compact_domain_prefix})"); do
        ipset destroy "$set_name" >/dev/null 2>&1
    done
}

# Main function to add rules and routes
add_routes() {
    echo "Configuring DNS and policy-based routing..."

    # 1. Cleanup old rules to ensure a fresh start
    cleanup_firewall
    ip route flush table "$TABLE_NUM" >/dev/null 2>&1
    ipset destroy "$IPSET_NAME" >/dev/null 2>&1

    # 2. Create a new ipset that will store our IPs
    echo "--> Creating ipset '$IPSET_NAME'..."
    ipset create "$IPSET_NAME" hash:ip timeout 3600

    # 3. Setup dnsmasq to populate the ipset automatically
    echo "--> Generating dnsmasq config to populate ipset..."
    mkdir -p "$(dirname "$DNSMASQ_CONFIG_FILE")"

    # Start with an empty file
    > "$DNSMASQ_CONFIG_FILE"

    # 3b. Create per-domain accounting chain
    echo "--> Creating domain stats accounting chain '$DOMAIN_STATS_CHAIN'..."
    iptables -N "$DOMAIN_STATS_CHAIN" 2>/dev/null
    compact_domain_prefix="vd_$(short_hash "$IPSET_NAME")_"

    grep -vE '^\s*#|^\s*$' "$DOMAIN_LIST" | while IFS= read -r domain_raw; do
        domain=$(echo "$domain_raw" | tr -d '\r')
        if [ -z "$domain" ]; then continue; fi

        # Shared ipset for routing
        echo "ipset=/$domain/$IPSET_NAME" >> "$DNSMASQ_CONFIG_FILE"

        # Per-domain ipset for traffic accounting
        # Use a short hash to keep ipset name under 31 chars
        dom_hash=$(short_hash "${IPSET_NAME}:${domain}")
        dom_set="${compact_domain_prefix}${dom_hash}"
        ipset create "$dom_set" hash:ip timeout 3600 2>/dev/null
        echo "ipset=/$domain/$dom_set" >> "$DNSMASQ_CONFIG_FILE"

        # Add iptables accounting rule with domain name in comment
        iptables -A "$DOMAIN_STATS_CHAIN" -m set --match-set "$dom_set" dst -m comment --comment "$domain" >/dev/null 2>&1
    done

    # Add the dnsmasq config directory if it's not already in the main config
    if ! grep -q "conf-dir=/tmp/dnsmasq.d" /etc/dnsmasq.conf; then
        echo "conf-dir=/tmp/dnsmasq.d,user=root" >> /etc/dnsmasq.conf
    fi

    echo "--> Restarting dnsmasq to apply new config..."
    /etc/init.d/dnsmasq restart

    # 3c. Prime ipsets immediately from current DNS answers.
    echo "--> Priming ipsets with current DNS answers..."
    grep -vE '^\s*#|^\s*$' "$DOMAIN_LIST" | while IFS= read -r domain_raw; do
        domain=$(echo "$domain_raw" | tr -d '\r')
        if [ -z "$domain" ]; then continue; fi

        dom_hash=$(short_hash "${IPSET_NAME}:${domain}")
        dom_set="${compact_domain_prefix}${dom_hash}"

        for ip in $(resolve_domain_ips "$domain"); do
            ipset add "$IPSET_NAME" "$ip" timeout 3600 -exist >/dev/null 2>&1
            ipset add "$dom_set" "$ip" timeout 3600 -exist >/dev/null 2>&1
        done
    done

    # 4. Create a rule to mark packets destined for our (now dynamic) ipset
    echo "--> Inserting iptables mangle rule at the top..."
    iptables -t mangle -I PREROUTING -i "$LAN_IFACE" -m set --match-set "$IPSET_NAME" dst -j MARK --set-mark "$FWMARK"

    # 5. Add forwarding and NAT rules to allow traffic into the tunnel
    echo "--> Inserting firewall FORWARD and NAT rules at the top..."
    iptables -I "$FW_ZONE_CHAIN" -i "$LAN_IFACE" -o "$VPN_IFACE" -j ACCEPT
    if [ "$VPN_MASQUERADE" = "1" ]; then
        iptables -t nat -I POSTROUTING -o "$VPN_IFACE" -j MASQUERADE
    fi

    # 5b. Insert domain stats accounting chain into FORWARD
    echo "--> Inserting domain stats accounting jump..."
    iptables -I FORWARD -o "$VPN_IFACE" -j "$DOMAIN_STATS_CHAIN" 2>/dev/null

    # 6. Create a new routing table for the VPN gateway
    echo "--> Creating new route for VPN in table '$TABLE_NUM'..."
    if [ "$VPN_ROUTE_MODE" = "dev" ]; then
        ip route add default dev "$VPN_IFACE" table "$TABLE_NUM"
    else
        ip route add default via "$VPN_GATEWAY" dev "$VPN_IFACE" table "$TABLE_NUM"
    fi

    # 7. Create a rule to use the new table for marked packets
    echo "--> Adding ip rule for marked packets..."
    ip rule add fwmark "$FWMARK" table "$TABLE_NUM"

    # 8. Flush routing cache
    ip route flush cache

    echo "Configuration complete. IP set will now be populated dynamically by DNS queries."
}

# Main function to delete all rules
delete_routes() {
    echo "Deleting all routing rules and firewall marks..."

    # 1. Cleanup firewall and routing rules
    cleanup_firewall

    # 2. Flush the custom routing table
    echo "--> Flushing route table '$TABLE_NUM'..."
    ip route flush table "$TABLE_NUM" >/dev/null 2>&1

    # 3. Destroy the ipset
    echo "--> Destroying ipset '$IPSET_NAME'..."
    ipset destroy "$IPSET_NAME" >/dev/null 2>&1

    # 4. Remove dnsmasq config and restart
    echo "--> Removing dnsmasq config and restarting..."
    rm -f "$DNSMASQ_CONFIG_FILE"
    /etc/init.d/dnsmasq restart

    # 5. Flush routing cache
    ip route flush cache

    echo "Cleanup complete."
}

case "$1" in
    add)
        add_routes
        ;;
    del|delete)
        delete_routes
        ;;
    *)
        echo "Usage: $0 {add|del}"
        exit 1
        ;;
esac

exit 0
