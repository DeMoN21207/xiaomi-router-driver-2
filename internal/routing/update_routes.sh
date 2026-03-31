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
DNSMASQ_RESTART_LOG="${DNSMASQ_RESTART_LOG:-/tmp/vpn-manager-dnsmasq-restart.log}"
PRIME_MAX_DOMAINS="${PRIME_MAX_DOMAINS:-64}"
DOMAIN_STATS_MAX_DOMAINS="${DOMAIN_STATS_MAX_DOMAINS:-256}"
# --- END OF SETTINGS ---

ENABLE_DOMAIN_STATS=1

short_hash() {
    value="$1"
    hash=$(printf '%s' "$value" | md5sum 2>/dev/null | cut -c1-8)
    if [ -z "$hash" ]; then
        hash=$(printf '%s' "$value" | cksum | cut -d' ' -f1 | cut -c1-8)
    fi
    printf '%s' "$hash"
}

legacy_domain_prefix="vpn_d_${IPSET_NAME}_"
compact_domain_prefix="vd_$(short_hash "$IPSET_NAME")_"

resolve_domain_ips() {
    domain="$1"
    nslookup "$domain" 127.0.0.1 2>/dev/null \
        | awk '/^Name:/ {capture=1; next} capture && /^Address [0-9]+: / {print $3} capture && /^Address: / {print $2}' \
        | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' \
        | sort -u
}

restart_dnsmasq() {
    if [ ! -x /etc/init.d/dnsmasq ]; then
        echo "Error: dnsmasq init script not found." >&2
        return 1
    fi

    mkdir -p "$(dirname "$DNSMASQ_RESTART_LOG")"
    : > "$DNSMASQ_RESTART_LOG"

    /etc/init.d/dnsmasq restart >/dev/null 2>"$DNSMASQ_RESTART_LOG"
}

count_active_domains() {
    grep -vE '^\s*#|^\s*$' "$DOMAIN_LIST" | wc -l | tr -d ' '
}

prime_ipsets() {
    count=0

    while IFS= read -r domain_raw || [ -n "$domain_raw" ]; do
        domain=$(echo "$domain_raw" | tr -d '\r')
        case "$domain" in
            ""|\#*) continue ;;
        esac
        if is_ipv4_entry "$domain"; then
            continue
        fi

        count=$((count + 1))
        if [ "${PRIME_MAX_DOMAINS:-0}" -gt 0 ] && [ "$count" -gt "$PRIME_MAX_DOMAINS" ]; then
            echo "--> Priming capped at ${PRIME_MAX_DOMAINS} domains; remaining entries will populate dynamically."
            break
        fi

        for ip in $(resolve_domain_ips "$domain"); do
            ipset add "$IPSET_NAME" "$ip" timeout 3600 -exist >/dev/null 2>&1
            if [ "$ENABLE_DOMAIN_STATS" = "1" ]; then
                dom_hash=$(short_hash "${IPSET_NAME}:${domain}")
                dom_set="${compact_domain_prefix}${dom_hash}"
                ipset add "$dom_set" "$ip" timeout 3600 -exist >/dev/null 2>&1
            fi
        done
    done < "$DOMAIN_LIST"
}

is_ipv4_entry() {
    printf '%s' "$1" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+(/[0-9]{1,2})?$'
}

count_domain_entries() {
    count=0
    while IFS= read -r entry_raw || [ -n "$entry_raw" ]; do
        entry=$(echo "$entry_raw" | tr -d '\r')
        case "$entry" in
            ""|\#*) continue ;;
        esac
        if is_ipv4_entry "$entry"; then
            continue
        fi
        count=$((count + 1))
    done < "$DOMAIN_LIST"
    printf '%s' "$count"
}

prime_static_entries() {
    while IFS= read -r entry_raw || [ -n "$entry_raw" ]; do
        entry=$(echo "$entry_raw" | tr -d '\r')
        case "$entry" in
            ""|\#*) continue ;;
        esac
        if ! is_ipv4_entry "$entry"; then
            continue
        fi

        ipset add "$IPSET_NAME" "$entry" timeout 0 -exist >/dev/null 2>&1
    done < "$DOMAIN_LIST"
}

shared_ipset_ready() {
    if ! ipset list "$IPSET_NAME" >/dev/null 2>&1; then
        return 1
    fi
    if ! ipset list "$IPSET_NAME" 2>/dev/null | grep -F "Type: hash:net" >/dev/null 2>&1; then
        return 1
    fi
    return 0
}

base_routing_ready() {
    if ! iptables -t mangle -C PREROUTING -i "$LAN_IFACE" -m set --match-set "$IPSET_NAME" dst -j MARK --set-mark "$FWMARK" >/dev/null 2>&1; then
        return 1
    fi
    if ! iptables -C "$FW_ZONE_CHAIN" -i "$LAN_IFACE" -o "$VPN_IFACE" -j ACCEPT >/dev/null 2>&1; then
        return 1
    fi
    if [ "$VPN_MASQUERADE" = "1" ] && ! iptables -t nat -C POSTROUTING -o "$VPN_IFACE" -j MASQUERADE >/dev/null 2>&1; then
        return 1
    fi

    if [ "$VPN_ROUTE_MODE" = "dev" ]; then
        route_match="default dev $VPN_IFACE"
    else
        route_match="default via $VPN_GATEWAY dev $VPN_IFACE"
    fi
    if ! ip route show table "$TABLE_NUM" 2>/dev/null | grep -F "$route_match" >/dev/null 2>&1; then
        return 1
    fi
    if ! ip rule show 2>/dev/null | grep -F "fwmark $FWMARK lookup $TABLE_NUM" >/dev/null 2>&1; then
        return 1
    fi

    return 0
}

# --- SCRIPT LOGIC ---
# Check for required utilities
for cmd in ip iptables ipset tr grep; do
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

    cleanup_domain_stats
}

cleanup_domain_stats() {
    # Domain stats accounting chain cleanup
    for stats_chain in "$DOMAIN_STATS_CHAIN" "$LEGACY_DOMAIN_STATS_CHAIN"; do
        if [ -z "$stats_chain" ]; then continue; fi
        iptables -D FORWARD -o "$VPN_IFACE" -j "$stats_chain" >/dev/null 2>&1
        iptables -F "$stats_chain" >/dev/null 2>&1
        iptables -X "$stats_chain" >/dev/null 2>&1
    done

    # Destroy per-domain ipsets (legacy and compact prefixes)
    for set_name in $(ipset list -n 2>/dev/null | grep -E "^(${legacy_domain_prefix}|${compact_domain_prefix})"); do
        ipset destroy "$set_name" >/dev/null 2>&1
    done
}

ensure_dnsmasq_conf_dir() {
    mkdir -p "$(dirname "$DNSMASQ_CONFIG_FILE")"
    if ! grep -q "conf-dir=/tmp/dnsmasq.d" /etc/dnsmasq.conf; then
        echo "conf-dir=/tmp/dnsmasq.d,user=root" >> /etc/dnsmasq.conf
    fi
}

render_dnsmasq_config() {
    echo "--> Generating dnsmasq config to populate ipset..."
    ensure_dnsmasq_conf_dir

    # Start with an empty file
    > "$DNSMASQ_CONFIG_FILE"

    domain_count=$(count_domain_entries)
    ENABLE_DOMAIN_STATS=1
    if [ "${DOMAIN_STATS_MAX_DOMAINS:-0}" -gt 0 ] && [ "$domain_count" -gt "$DOMAIN_STATS_MAX_DOMAINS" ]; then
        ENABLE_DOMAIN_STATS=0
        echo "--> Domain stats disabled for ${domain_count} domains (limit: ${DOMAIN_STATS_MAX_DOMAINS})."
    fi

    # Create per-domain accounting chain only when needed.
    if [ "$ENABLE_DOMAIN_STATS" = "1" ]; then
        echo "--> Creating domain stats accounting chain '$DOMAIN_STATS_CHAIN'..."
        iptables -N "$DOMAIN_STATS_CHAIN" 2>/dev/null
    fi

    grep -vE '^\s*#|^\s*$' "$DOMAIN_LIST" | while IFS= read -r domain_raw; do
        domain=$(echo "$domain_raw" | tr -d '\r')
        if [ -z "$domain" ]; then continue; fi
        if is_ipv4_entry "$domain"; then continue; fi

        # Shared ipset for routing
        echo "ipset=/$domain/$IPSET_NAME" >> "$DNSMASQ_CONFIG_FILE"

        if [ "$ENABLE_DOMAIN_STATS" = "1" ]; then
            # Per-domain ipset for traffic accounting
            # Use a short hash to keep ipset name under 31 chars
            dom_hash=$(short_hash "${IPSET_NAME}:${domain}")
            dom_set="${compact_domain_prefix}${dom_hash}"
            ipset create "$dom_set" hash:ip family inet timeout 3600 2>/dev/null
            echo "ipset=/$domain/$dom_set" >> "$DNSMASQ_CONFIG_FILE"

            # Add iptables accounting rule with domain name in comment
            iptables -A "$DOMAIN_STATS_CHAIN" -m set --match-set "$dom_set" dst -m comment --comment "$domain" >/dev/null 2>&1
        fi
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
    ipset create "$IPSET_NAME" hash:net family inet timeout 3600

    # 3. Setup dnsmasq to populate the ipset automatically
    render_dnsmasq_config

    echo "--> Restarting dnsmasq to apply new config..."
    if ! restart_dnsmasq; then
        echo "Error: dnsmasq restart failed." >&2
        if [ -s "$DNSMASQ_RESTART_LOG" ]; then
            cat "$DNSMASQ_RESTART_LOG" >&2
        fi
        exit 1
    fi

    # 3c. Prime static IP ranges and current DNS answers.
    echo "--> Priming static IP routes..."
    prime_static_entries

    echo "--> Priming ipsets with current DNS answers..."
    prime_ipsets

    # 4. Create a rule to mark packets destined for our (now dynamic) ipset
    echo "--> Inserting iptables mangle rule at the top..."
    if ! iptables -t mangle -I PREROUTING -i "$LAN_IFACE" -m set --match-set "$IPSET_NAME" dst -j MARK --set-mark "$FWMARK"; then
        echo "Error: failed to insert the mangle mark rule." >&2
        exit 1
    fi

    # 5. Add forwarding and NAT rules to allow traffic into the tunnel
    echo "--> Inserting firewall FORWARD and NAT rules at the top..."
    if ! iptables -I "$FW_ZONE_CHAIN" -i "$LAN_IFACE" -o "$VPN_IFACE" -j ACCEPT; then
        echo "Error: failed to insert the FORWARD rule into chain '$FW_ZONE_CHAIN'." >&2
        exit 1
    fi
    if [ "$VPN_MASQUERADE" = "1" ]; then
        if ! iptables -t nat -I POSTROUTING -o "$VPN_IFACE" -j MASQUERADE; then
            echo "Error: failed to insert the MASQUERADE rule for '$VPN_IFACE'." >&2
            exit 1
        fi
    fi

    # 5b. Insert domain stats accounting chain into FORWARD
    if [ "$ENABLE_DOMAIN_STATS" = "1" ]; then
        echo "--> Inserting domain stats accounting jump..."
        iptables -I FORWARD -o "$VPN_IFACE" -j "$DOMAIN_STATS_CHAIN" 2>/dev/null
    fi

    # 6. Create a new routing table for the VPN gateway
    echo "--> Creating new route for VPN in table '$TABLE_NUM'..."
    if [ "$VPN_ROUTE_MODE" = "dev" ]; then
        if ! ip route add default dev "$VPN_IFACE" table "$TABLE_NUM"; then
            echo "Error: failed to create the default route via interface '$VPN_IFACE' in table '$TABLE_NUM'." >&2
            exit 1
        fi
    else
        if ! ip route add default via "$VPN_GATEWAY" dev "$VPN_IFACE" table "$TABLE_NUM"; then
            echo "Error: failed to create the default route via gateway '$VPN_GATEWAY' on '$VPN_IFACE' in table '$TABLE_NUM'." >&2
            exit 1
        fi
    fi

    # 7. Create a rule to use the new table for marked packets
    echo "--> Adding ip rule for marked packets..."
    if ! ip rule add fwmark "$FWMARK" table "$TABLE_NUM"; then
        echo "Error: failed to add ip rule for mark '$FWMARK' and table '$TABLE_NUM'." >&2
        exit 1
    fi

    # 8. Flush routing cache
    ip route flush cache

    echo "Configuration complete. IP set will now be populated dynamically by DNS queries."
}

sync_routes() {
    echo "Refreshing DNS and policy-based routing..."

    if ! shared_ipset_ready; then
        echo "--> Shared ipset '$IPSET_NAME' is missing or outdated; falling back to full add."
        add_routes
        return
    fi
    if ! base_routing_ready; then
        echo "--> Base routing rules are missing; falling back to full add."
        add_routes
        return
    fi

    cleanup_domain_stats
    ipset flush "$IPSET_NAME" >/dev/null 2>&1

    render_dnsmasq_config

    echo "--> Restarting dnsmasq to apply new config..."
    if ! restart_dnsmasq; then
        echo "Error: dnsmasq restart failed." >&2
        if [ -s "$DNSMASQ_RESTART_LOG" ]; then
            cat "$DNSMASQ_RESTART_LOG" >&2
        fi
        exit 1
    fi

    echo "--> Priming static IP routes..."
    prime_static_entries

    echo "--> Priming ipsets with current DNS answers..."
    prime_ipsets

    if [ "$ENABLE_DOMAIN_STATS" = "1" ]; then
        echo "--> Restoring domain stats accounting jump..."
        iptables -I FORWARD -o "$VPN_IFACE" -j "$DOMAIN_STATS_CHAIN" 2>/dev/null
    fi

    ip route flush cache
    echo "Domain routing refreshed."
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
    if ! restart_dnsmasq; then
        echo "Error: dnsmasq restart failed." >&2
        if [ -s "$DNSMASQ_RESTART_LOG" ]; then
            cat "$DNSMASQ_RESTART_LOG" >&2
        fi
        exit 1
    fi

    # 5. Flush routing cache
    ip route flush cache

    echo "Cleanup complete."
}

case "$1" in
    add)
        add_routes
        ;;
    sync|refresh)
        sync_routes
        ;;
    del|delete)
        delete_routes
        ;;
    *)
        echo "Usage: $0 {add|sync|del}"
        exit 1
        ;;
esac

exit 0
