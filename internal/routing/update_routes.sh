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
DNSMASQ_MAIN_CONFIG="${DNSMASQ_MAIN_CONFIG:-/etc/dnsmasq.conf}"
DNS_PROXY_SERVER="${DNS_PROXY_SERVER:-}"
DOMAIN_STATS_CHAIN="${DOMAIN_STATS_CHAIN:-VDS_${IPSET_NAME}}"
LEGACY_DOMAIN_STATS_CHAIN="${LEGACY_DOMAIN_STATS_CHAIN:-VPN_DOM_STATS_${IPSET_NAME}}"
DNSMASQ_RESTART_LOG="${DNSMASQ_RESTART_LOG:-/tmp/vpn-manager-dnsmasq-restart.log}"
PRIME_MAX_DOMAINS="${PRIME_MAX_DOMAINS:-0}"
DNS_PRIME_LOOKUP_TIMEOUT_SECONDS="${DNS_PRIME_LOOKUP_TIMEOUT_SECONDS:-2}"
DNS_PRIME_SERVERS="${DNS_PRIME_SERVERS:-1.1.1.1,8.8.8.8,9.9.9.9}"
DOMAIN_STATS_MAX_DOMAINS="${DOMAIN_STATS_MAX_DOMAINS:-128}"
IPSET_TIMEOUT="${IPSET_TIMEOUT:-1800}"
IPSET_FLUSH_ON_SYNC="${IPSET_FLUSH_ON_SYNC:-0}"
ROUTING_LOCK_FILE="${ROUTING_LOCK_FILE:-/tmp/vpn-manager-routing.lock}"
ROUTING_LOCK_WAIT_SECONDS="${ROUTING_LOCK_WAIT_SECONDS:-30}"
IPTABLES_WAIT_SECONDS="${IPTABLES_WAIT_SECONDS:-5}"
MSS_CLAMP="${MSS_CLAMP:-1}"
MSS_VALUE="${MSS_VALUE:-0}" # 0 means --clamp-mss-to-pmtu
DNS_HIJACK="${DNS_HIJACK:-1}"
IPV6_MODE="${IPV6_MODE:-warn}" # warn | allow | disable
SAFE_DIRECT_ON_ROUTE_FAIL="${SAFE_DIRECT_ON_ROUTE_FAIL:-0}"
CONNTRACK_FLUSH_ON_APPLY="${CONNTRACK_FLUSH_ON_APPLY:-0}"
# --- END OF SETTINGS ---

ENABLE_DOMAIN_STATS=1
DNSMASQ_CONFIG_CHANGED=0
DNSMASQ_CONFIG_BASENAME="${DNSMASQ_CONFIG_FILE##*/}"
DNSMASQ_CONFIG_BACKUP="${DNSMASQ_CONFIG_BACKUP:-/tmp/vpn-manager-dnsmasq/${DNSMASQ_CONFIG_BASENAME}.bak}"

short_hash() {
    value="$1"
    hash=""
    if command -v md5sum >/dev/null 2>&1; then
        hash=$(printf '%s' "$value" | md5sum 2>/dev/null | cut -c1-8)
    fi
    if [ -z "$hash" ] && command -v cksum >/dev/null 2>&1; then
        hash=$(printf '%s' "$value" | cksum | cut -d' ' -f1 | cut -c1-8)
    fi
    if [ -z "$hash" ]; then
        hash=$(printf '%s' "$value" | tr -cd '[:alnum:]' | cut -c1-8)
    fi
    if [ -z "$hash" ]; then
        hash="00000000"
    fi
    printf '%s' "$hash"
}

legacy_domain_prefix="vpn_d_${IPSET_NAME}_"
compact_domain_prefix="vd_$(short_hash "$IPSET_NAME")_"
MSS_CHAIN="VMS_$(short_hash "${IPSET_NAME}:${FWMARK}")"

acquire_routing_lock() {
    if command -v flock >/dev/null 2>&1; then
        exec 9>"$ROUTING_LOCK_FILE" || return 1
        if flock -w "$ROUTING_LOCK_WAIT_SECONDS" 9 >/dev/null 2>&1; then
            ROUTING_LOCK_KIND="flock"
            return 0
        fi
        if flock -n 9 >/dev/null 2>&1; then
            ROUTING_LOCK_KIND="flock"
            return 0
        fi
        echo "Error: another routing update is already running." >&2
        return 1
    fi

    ROUTING_LOCK_DIR="${ROUTING_LOCK_FILE}.d"
    deadline=$(( $(date +%s 2>/dev/null || echo 0) + ROUTING_LOCK_WAIT_SECONDS ))
    while ! mkdir "$ROUTING_LOCK_DIR" >/dev/null 2>&1; do
        now=$(date +%s 2>/dev/null || echo 0)
        if [ "$now" -ge "$deadline" ]; then
            echo "Error: another routing update is already running." >&2
            return 1
        fi
        sleep 1
    done
    ROUTING_LOCK_KIND="dir"
    return 0
}

release_routing_lock() {
    if [ "$ROUTING_LOCK_KIND" = "dir" ] && [ -n "$ROUTING_LOCK_DIR" ]; then
        rmdir "$ROUTING_LOCK_DIR" >/dev/null 2>&1 || true
    fi
    if [ "$ROUTING_LOCK_KIND" = "flock" ]; then
        exec 9>&- 2>/dev/null || true
    fi
}

setup_iptables_wait() {
    IPTABLES_HAS_WAIT=0
    if command iptables -w "$IPTABLES_WAIT_SECONDS" -L -n >/dev/null 2>&1; then
        IPTABLES_HAS_WAIT=1
    fi
}

iptables() {
    if [ "$IPTABLES_HAS_WAIT" = "1" ]; then
        command iptables -w "$IPTABLES_WAIT_SECONDS" "$@"
    else
        command iptables "$@"
    fi
}

run_with_timeout() {
    seconds="$1"
    shift

    if ! command -v timeout >/dev/null 2>&1 || [ "${seconds:-0}" -le 0 ] 2>/dev/null; then
        "$@"
        return
    fi

    if [ "${TIMEOUT_NEEDS_T_FLAG:-}" = "" ]; then
        if timeout --help 2>&1 | grep -q -- '\[-t SECS\]'; then
            TIMEOUT_NEEDS_T_FLAG=1
        else
            TIMEOUT_NEEDS_T_FLAG=0
        fi
    fi

    if [ "$TIMEOUT_NEEDS_T_FLAG" = "1" ]; then
        timeout -t "$seconds" "$@"
    else
        timeout "$seconds" "$@"
    fi
}

resolve_domain_ips() {
    domain="$1"
    dns_server="${2:-127.0.0.1}"
    if command -v timeout >/dev/null 2>&1 && [ "${DNS_PRIME_LOOKUP_TIMEOUT_SECONDS:-0}" -gt 0 ] 2>/dev/null; then
        run_with_timeout "$DNS_PRIME_LOOKUP_TIMEOUT_SECONDS" nslookup "$domain" "$dns_server" 2>/dev/null
    else
        nslookup "$domain" "$dns_server" 2>/dev/null
    fi \
        | awk '/^Name:/ {capture=1; next} capture && /^Address [0-9]+: / {print $3} capture && /^Address: / {print $2}' \
        | grep -E '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$' \
        | sort -u
}

prime_domain_ips() {
    domain="$1"
    collected=""

    for ip in $(resolve_domain_ips "$domain" "127.0.0.1"); do
        case " $collected " in
            *" $ip "*) ;;
            *)
                collected="$collected $ip"
                ipset add "$IPSET_NAME" "$ip" timeout "$IPSET_TIMEOUT" -exist >/dev/null 2>&1
                if [ "$ENABLE_DOMAIN_STATS" = "1" ]; then
                    dom_hash=$(short_hash "${IPSET_NAME}:${domain}")
                    dom_set="${compact_domain_prefix}${dom_hash}"
                    ipset add "$dom_set" "$ip" timeout "$IPSET_TIMEOUT" -exist >/dev/null 2>&1
                fi
                ;;
        esac
    done

    for dns_server in $(printf '%s\n' "$DNS_PRIME_SERVERS" | tr ',;' ' '); do
        dns_server=$(echo "$dns_server" | tr -d '\r')
        if [ -z "$dns_server" ] || [ "$dns_server" = "127.0.0.1" ]; then
            continue
        fi

        for ip in $(resolve_domain_ips "$domain" "$dns_server"); do
            case " $collected " in
                *" $ip "*) ;;
                *)
                    collected="$collected $ip"
                    ipset add "$IPSET_NAME" "$ip" timeout "$IPSET_TIMEOUT" -exist >/dev/null 2>&1
                    if [ "$ENABLE_DOMAIN_STATS" = "1" ]; then
                        dom_hash=$(short_hash "${IPSET_NAME}:${domain}")
                        dom_set="${compact_domain_prefix}${dom_hash}"
                        ipset add "$dom_set" "$ip" timeout "$IPSET_TIMEOUT" -exist >/dev/null 2>&1
                    fi
                    ;;
            esac
        done
    done
}

domain_stats_enabled() {
    domain_count="$1"
    mode="$(printf '%s' "${VPN_MANAGER_DOMAIN_STATS:-${DOMAIN_STATS:-}}" | tr 'A-Z' 'a-z')"
    case "$mode" in
        0|off|false|disabled)
            return 1
            ;;
        1|on|true|enabled)
            return 0
            ;;
    esac

    if [ "${DOMAIN_STATS_MAX_DOMAINS:-0}" -le 0 ] 2>/dev/null; then
        return 0
    fi
    [ "$domain_count" -le "$DOMAIN_STATS_MAX_DOMAINS" ]
}

dnsmasq_control() {
    action="$1"
    mkdir -p "$(dirname "$DNSMASQ_RESTART_LOG")"
    : > "$DNSMASQ_RESTART_LOG"

    if [ -x /etc/init.d/dnsmasq ]; then
        /etc/init.d/dnsmasq "$action" >/dev/null 2>"$DNSMASQ_RESTART_LOG"
        return $?
    fi

    if [ "$action" = "reload" ] || [ "$action" = "restart" ]; then
        if command -v pidof >/dev/null 2>&1; then
            pids=$(pidof dnsmasq 2>/dev/null)
            if [ -n "$pids" ]; then
                kill -HUP $pids >/dev/null 2>"$DNSMASQ_RESTART_LOG"
                return $?
            fi
        fi
    fi

    echo "dnsmasq control path not found" > "$DNSMASQ_RESTART_LOG"
    return 1
}

reload_dnsmasq() {
    mode="${DNSMASQ_RELOAD_MODE:-auto}"
    case "$mode" in
        restart)
            dnsmasq_control restart
            return $?
            ;;
        reload)
            dnsmasq_control reload
            return $?
            ;;
    esac

    if dnsmasq_control reload; then
        return 0
    fi
    if command -v pidof >/dev/null 2>&1; then
        pids=$(pidof dnsmasq 2>/dev/null)
        if [ -n "$pids" ] && kill -HUP $pids >/dev/null 2>>"$DNSMASQ_RESTART_LOG"; then
            return 0
        fi
    fi
    dnsmasq_control restart
}

apply_dnsmasq_changes() {
    if [ "$DNSMASQ_CONFIG_CHANGED" != "1" ]; then
        echo "--> dnsmasq config unchanged; reload skipped."
        return 0
    fi

    echo "--> Restarting dnsmasq to apply config..."
    # Some OpenWrt dnsmasq builds do not reread conf-dir snippets on reload/SIGHUP.
    if dnsmasq_control restart; then
        return 0
    fi

    echo "Error: dnsmasq restart failed." >&2
    if [ -s "$DNSMASQ_RESTART_LOG" ]; then
        cat "$DNSMASQ_RESTART_LOG" >&2
    fi

    if [ -f "$DNSMASQ_CONFIG_BACKUP" ]; then
        echo "--> Restoring previous dnsmasq config after failed restart." >&2
        cp "$DNSMASQ_CONFIG_BACKUP" "$DNSMASQ_CONFIG_FILE" >/dev/null 2>&1 || true
        dnsmasq_control restart >/dev/null 2>&1 || true
    fi
    return 1
}

count_active_domains() {
    grep -vE '^\s*#|^\s*$' "$DOMAIN_LIST" | wc -l | tr -d ' '
}

prime_ipsets() {
    if [ "${PRIME_MAX_DOMAINS:-0}" -le 0 ] 2>/dev/null; then
        echo "--> Priming disabled; dnsmasq/ipset will populate entries on demand."
        return 0
    fi

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

        prime_domain_ips "$domain"
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

prime_static_entries_fallback() {
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

prime_static_entries_batch() {
    tmp_restore="/tmp/vpn-manager-ipset.$$"
    add_count=0

    : > "$tmp_restore" || return 1
    printf 'create %s hash:net family inet timeout %s -exist\n' "$IPSET_NAME" "$IPSET_TIMEOUT" >> "$tmp_restore" || {
        rm -f "$tmp_restore"
        return 1
    }

    while IFS= read -r entry_raw || [ -n "$entry_raw" ]; do
        entry=$(echo "$entry_raw" | tr -d '\r')
        case "$entry" in
            ""|\#*) continue ;;
        esac
        if ! is_ipv4_entry "$entry"; then
            continue
        fi

        printf 'add %s %s timeout 0 -exist\n' "$IPSET_NAME" "$entry" >> "$tmp_restore" || {
            rm -f "$tmp_restore"
            return 1
        }
        add_count=$((add_count + 1))
    done < "$DOMAIN_LIST"

    if [ "$add_count" -eq 0 ]; then
        rm -f "$tmp_restore"
        return 0
    fi

    if ipset restore -exist < "$tmp_restore" >/dev/null 2>&1; then
        rm -f "$tmp_restore"
        return 0
    fi

    rm -f "$tmp_restore"
    return 1
}

prime_static_entries() {
    if prime_static_entries_batch; then
        return 0
    fi

    echo "Warning: batch ipset restore failed for static entries; falling back to per-entry add." >&2
    prime_static_entries_fallback
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

ensure_shared_ipset() {
    if shared_ipset_ready; then
        ipset flush "$IPSET_NAME" >/dev/null 2>&1 || return 1
        return 0
    fi

    ipset destroy "$IPSET_NAME" >/dev/null 2>&1 || true
    ipset create "$IPSET_NAME" hash:net family inet timeout "$IPSET_TIMEOUT" -exist
}

iptables_chain_exists() {
    iptables -nL "$1" >/dev/null 2>&1
}

resolve_forward_chain() {
    if [ -n "$FW_ZONE_CHAIN" ] && iptables_chain_exists "$FW_ZONE_CHAIN"; then
        printf '%s\n' "$FW_ZONE_CHAIN"
        return 0
    fi
    printf '%s\n' "FORWARD"
}

enable_ipv4_forwarding() {
    if [ -w /proc/sys/net/ipv4/ip_forward ]; then
        echo 1 > /proc/sys/net/ipv4/ip_forward 2>/dev/null || true
    fi
}

handle_ipv6_mode() {
    case "$(printf '%s' "$IPV6_MODE" | tr 'A-Z' 'a-z')" in
        allow|off|0|false)
            return 0
            ;;
        disable|block)
            changed=0
            for path in \
                /proc/sys/net/ipv6/conf/all/disable_ipv6 \
                /proc/sys/net/ipv6/conf/default/disable_ipv6 \
                "/proc/sys/net/ipv6/conf/${LAN_IFACE}/disable_ipv6"; do
                if [ -w "$path" ]; then
                    echo 1 > "$path" 2>/dev/null && changed=1
                fi
            done
            if [ "$changed" = "1" ]; then
                echo "--> IPv6 disabled for routed LAN path to avoid IPv6 bypass/leaks."
            else
                echo "Warning: IPv6 disable requested but sysctl paths were not writable." >&2
            fi
            ;;
        warn|*)
            if [ -r /proc/sys/net/ipv6/conf/all/disable_ipv6 ]; then
                disabled=$(cat /proc/sys/net/ipv6/conf/all/disable_ipv6 2>/dev/null)
                if [ "$disabled" = "0" ]; then
                    echo "Warning: IPv6 is enabled. Domain VPN routing is IPv4-only unless you disable IPv6 or add IPv6 policy routing." >&2
                fi
            fi
            ;;
    esac
}

cleanup_forward_accept_rules() {
    for chain in "$FW_ZONE_CHAIN" FORWARD; do
        if [ -z "$chain" ]; then continue; fi
        if ! iptables_chain_exists "$chain"; then continue; fi
        while iptables -D "$chain" -i "$LAN_IFACE" -o "$VPN_IFACE" -j ACCEPT >/dev/null 2>&1; do :; done
    done
}

cleanup_dns_hijack_rules() {
    while iptables -t nat -D PREROUTING -i "$LAN_IFACE" -p udp --dport 53 -j REDIRECT --to-ports 53 >/dev/null 2>&1; do :; done
    while iptables -t nat -D PREROUTING -i "$LAN_IFACE" -p tcp --dport 53 -j REDIRECT --to-ports 53 >/dev/null 2>&1; do :; done
}

ensure_dns_hijack_rules() {
    if [ "$DNS_HIJACK" != "1" ]; then
        return 0
    fi
    if ! iptables -t nat -C PREROUTING -i "$LAN_IFACE" -p udp --dport 53 -j REDIRECT --to-ports 53 >/dev/null 2>&1; then
        iptables -t nat -I PREROUTING -i "$LAN_IFACE" -p udp --dport 53 -j REDIRECT --to-ports 53 || return 1
    fi
    if ! iptables -t nat -C PREROUTING -i "$LAN_IFACE" -p tcp --dport 53 -j REDIRECT --to-ports 53 >/dev/null 2>&1; then
        iptables -t nat -I PREROUTING -i "$LAN_IFACE" -p tcp --dport 53 -j REDIRECT --to-ports 53 || return 1
    fi
    return 0
}

cleanup_mss_clamp_rules() {
    while iptables -t mangle -D FORWARD -j "$MSS_CHAIN" >/dev/null 2>&1; do :; done
    iptables -t mangle -F "$MSS_CHAIN" >/dev/null 2>&1 || true
    iptables -t mangle -X "$MSS_CHAIN" >/dev/null 2>&1 || true
}

append_mss_rule() {
    direction="$1"
    if [ "$direction" = "out" ]; then
        iface_arg="-o"
    else
        iface_arg="-i"
    fi

    if [ "${MSS_VALUE:-0}" -gt 0 ] 2>/dev/null; then
        iptables -t mangle -A "$MSS_CHAIN" "$iface_arg" "$VPN_IFACE" -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --set-mss "$MSS_VALUE"
    else
        iptables -t mangle -A "$MSS_CHAIN" "$iface_arg" "$VPN_IFACE" -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu
    fi
}

ensure_mss_clamp_rules() {
    if [ "$MSS_CLAMP" != "1" ]; then
        return 0
    fi

    cleanup_mss_clamp_rules
    iptables -t mangle -N "$MSS_CHAIN" 2>/dev/null || true
    append_mss_rule out || return 1
    append_mss_rule in || return 1

    if ! iptables -t mangle -C FORWARD -j "$MSS_CHAIN" >/dev/null 2>&1; then
        iptables -t mangle -I FORWARD -j "$MSS_CHAIN" || return 1
    fi
    return 0
}

cleanup_domain_stats_jumps() {
    stats_chain="$1"
    for chain in FORWARD "$FW_ZONE_CHAIN"; do
        if [ -z "$chain" ]; then continue; fi
        if ! iptables_chain_exists "$chain"; then continue; fi
        while iptables -D "$chain" -o "$VPN_IFACE" -j "$stats_chain" >/dev/null 2>&1; do :; done
        while iptables -D "$chain" -i "$VPN_IFACE" -j "$stats_chain" >/dev/null 2>&1; do :; done
    done
}

ensure_domain_stats_jumps() {
    if [ "$ENABLE_DOMAIN_STATS" != "1" ]; then
        return 0
    fi
    if ! iptables -C FORWARD -o "$VPN_IFACE" -j "$DOMAIN_STATS_CHAIN" >/dev/null 2>&1; then
        iptables -I FORWARD -o "$VPN_IFACE" -j "$DOMAIN_STATS_CHAIN" 2>/dev/null || return 1
    fi
    if ! iptables -C FORWARD -i "$VPN_IFACE" -j "$DOMAIN_STATS_CHAIN" >/dev/null 2>&1; then
        iptables -I FORWARD -i "$VPN_IFACE" -j "$DOMAIN_STATS_CHAIN" 2>/dev/null || return 1
    fi
    return 0
}

ensure_vpn_route() {
    if ! ip link show "$VPN_IFACE" >/dev/null 2>&1; then
        echo "Error: VPN interface '$VPN_IFACE' is not present; refusing to mark traffic into a dead route." >&2
        return 1
    fi

    if [ "$VPN_ROUTE_MODE" = "dev" ]; then
        ip route replace default dev "$VPN_IFACE" table "$TABLE_NUM"
        return $?
    fi

    ip route replace default via "$VPN_GATEWAY" dev "$VPN_IFACE" table "$TABLE_NUM"
}

safe_direct_after_route_failure() {
    if [ "$SAFE_DIRECT_ON_ROUTE_FAIL" = "1" ]; then
        echo "--> Safe direct mode: removing stale marks so LAN traffic is not blackholed." >&2
        cleanup_firewall
        ip route flush table "$TABLE_NUM" >/dev/null 2>&1 || true
        ip route flush cache >/dev/null 2>&1 || true
    fi
}

validate_vpn_route_or_safe_direct() {
    if ensure_vpn_route; then
        return 0
    fi
    safe_direct_after_route_failure
    return 1
}

base_routing_ready() {
    forward_chain=$(resolve_forward_chain)
    if ! ip link show "$VPN_IFACE" >/dev/null 2>&1; then
        return 1
    fi
    if ! iptables -t mangle -C PREROUTING -i "$LAN_IFACE" -m set --match-set "$IPSET_NAME" dst -j MARK --set-mark "$FWMARK" >/dev/null 2>&1; then
        return 1
    fi
    if ! iptables -C "$forward_chain" -i "$LAN_IFACE" -o "$VPN_IFACE" -j ACCEPT >/dev/null 2>&1; then
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

cleanup_domain_stats() {
    # Domain stats accounting chain cleanup
    for stats_chain in "$DOMAIN_STATS_CHAIN" "$LEGACY_DOMAIN_STATS_CHAIN"; do
        if [ -z "$stats_chain" ]; then continue; fi
        cleanup_domain_stats_jumps "$stats_chain"
        iptables -F "$stats_chain" >/dev/null 2>&1
        iptables -X "$stats_chain" >/dev/null 2>&1
    done

    # Destroy per-domain ipsets (legacy and compact prefixes)
    for set_name in $(ipset list -n 2>/dev/null | grep -E "^(${legacy_domain_prefix}|${compact_domain_prefix})"); do
        ipset destroy "$set_name" >/dev/null 2>&1
    done
}

cleanup_firewall() {
    # Delete all duplicates left by interrupted/partial previous runs.
    while iptables -t mangle -D PREROUTING -i "$LAN_IFACE" -m set --match-set "$IPSET_NAME" dst -j MARK --set-mark "$FWMARK" >/dev/null 2>&1; do :; done
    while ip rule del fwmark "$FWMARK" table "$TABLE_NUM" >/dev/null 2>&1; do :; done

    cleanup_forward_accept_rules
    cleanup_dns_hijack_rules
    cleanup_mss_clamp_rules

    while iptables -t nat -D POSTROUTING -o "$VPN_IFACE" -j MASQUERADE >/dev/null 2>&1; do :; done

    cleanup_domain_stats
}

ensure_dnsmasq_conf_dir() {
    dnsmasq_conf_dir=$(dirname "$DNSMASQ_CONFIG_FILE")
    mkdir -p "$dnsmasq_conf_dir"
    if [ ! -f "$DNSMASQ_MAIN_CONFIG" ]; then
        : > "$DNSMASQ_MAIN_CONFIG" 2>/dev/null || return 0
    fi
    if ! grep -F "conf-dir=$dnsmasq_conf_dir" "$DNSMASQ_MAIN_CONFIG" >/dev/null 2>&1; then
        echo "conf-dir=$dnsmasq_conf_dir,user=root" >> "$DNSMASQ_MAIN_CONFIG"
    fi
}

dnsmasq_conf_references_live_ipset() {
    fragment="$1"
    for set_name in $(awk -F/ '/^ipset=\// {print $NF}' "$fragment" 2>/dev/null | tr ',' ' '); do
        [ -z "$set_name" ] && continue
        if ipset list "$set_name" >/dev/null 2>&1; then
            return 0
        fi
    done
    return 1
}

cleanup_dnsmasq_conf_fragments() {
    dnsmasq_conf_dir=$(dirname "$DNSMASQ_CONFIG_FILE")
    for fragment in "$dnsmasq_conf_dir"/vpn_dns*.conf "$dnsmasq_conf_dir"/vpn_dns*.conf.bak; do
        [ -e "$fragment" ] || continue
        if [ "$fragment" = "$DNSMASQ_CONFIG_FILE" ]; then
            continue
        fi
        case "$fragment" in
            *.conf.bak) ;;
            *)
                if dnsmasq_conf_references_live_ipset "$fragment"; then
                    continue
                fi
                ;;
        esac
        if rm -f "$fragment" >/dev/null 2>&1; then
            DNSMASQ_CONFIG_CHANGED=1
        fi
    done
}

render_dnsmasq_config() {
    echo "--> Generating dnsmasq config to populate ipset..."
    ensure_dnsmasq_conf_dir
    cleanup_dnsmasq_conf_fragments

    tmp_file="${DNSMASQ_CONFIG_FILE}.$$"
    : > "$tmp_file" || return 1

    domain_count=$(count_active_domains)
    ENABLE_DOMAIN_STATS=1
    if ! domain_stats_enabled "$domain_count"; then
        ENABLE_DOMAIN_STATS=0
        echo "--> Domain stats disabled for ${domain_count} domains."
    fi

    # Create per-domain accounting chain only when needed.
    if [ "$ENABLE_DOMAIN_STATS" = "1" ]; then
        echo "--> Creating domain stats accounting chain '$DOMAIN_STATS_CHAIN'..."
        iptables -N "$DOMAIN_STATS_CHAIN" 2>/dev/null || true
    fi

    grep -vE '^\s*#|^\s*$' "$DOMAIN_LIST" | while IFS= read -r domain_raw; do
        domain=$(echo "$domain_raw" | tr -d '\r')
        if [ -z "$domain" ]; then continue; fi

        if is_ipv4_entry "$domain"; then
            if [ "$ENABLE_DOMAIN_STATS" = "1" ]; then
                # Static IP/CIDR entries are already routed through the shared
                # ipset; direct rules let the traffic table show them by value.
                iptables -A "$DOMAIN_STATS_CHAIN" -d "$domain" -m comment --comment "${domain}|up" >/dev/null 2>&1 || true
                iptables -A "$DOMAIN_STATS_CHAIN" -s "$domain" -m comment --comment "${domain}|dn" >/dev/null 2>&1 || true
            fi
            continue
        fi

        ipset_targets="$IPSET_NAME"
        dom_set=""

        if [ "$ENABLE_DOMAIN_STATS" = "1" ]; then
            # Per-domain ipset for traffic accounting.
            # Use a short hash to keep ipset name under 31 chars.
            dom_hash=$(short_hash "${IPSET_NAME}:${domain}")
            dom_set="${compact_domain_prefix}${dom_hash}"
            ipset create "$dom_set" hash:ip family inet timeout "$IPSET_TIMEOUT" -exist 2>/dev/null
            ipset_targets="${ipset_targets},${dom_set}"
        fi

        echo "ipset=/$domain/$ipset_targets" >> "$tmp_file"
        if [ -n "$DNS_PROXY_SERVER" ]; then
            echo "server=/$domain/$DNS_PROXY_SERVER" >> "$tmp_file"
        fi

        if [ "$ENABLE_DOMAIN_STATS" = "1" ]; then
            # Accounting rules: upload (LAN->VPN, dst=server) and download (VPN->LAN, src=server).
            iptables -A "$DOMAIN_STATS_CHAIN" -m set --match-set "$dom_set" dst -m comment --comment "${domain}|up" >/dev/null 2>&1 || true
            iptables -A "$DOMAIN_STATS_CHAIN" -m set --match-set "$dom_set" src -m comment --comment "${domain}|dn" >/dev/null 2>&1 || true
        fi
    done

    if [ -f "$DNSMASQ_CONFIG_FILE" ] && command -v cmp >/dev/null 2>&1 && cmp -s "$tmp_file" "$DNSMASQ_CONFIG_FILE"; then
        rm -f "$tmp_file"
        return 0
    fi

    DNSMASQ_CONFIG_CHANGED=1
    if [ -f "$DNSMASQ_CONFIG_FILE" ]; then
        mkdir -p "$(dirname "$DNSMASQ_CONFIG_BACKUP")" >/dev/null 2>&1 || true
        cp "$DNSMASQ_CONFIG_FILE" "$DNSMASQ_CONFIG_BACKUP" >/dev/null 2>&1 || true
    else
        rm -f "$DNSMASQ_CONFIG_BACKUP" >/dev/null 2>&1 || true
    fi
    mv "$tmp_file" "$DNSMASQ_CONFIG_FILE"
}

flush_conntrack_if_requested() {
    if [ "$CONNTRACK_FLUSH_ON_APPLY" != "1" ]; then
        return 0
    fi
    if ! command -v conntrack >/dev/null 2>&1; then
        echo "--> conntrack cleanup requested but conntrack binary is not available."
        return 0
    fi
    echo "--> Flushing conntrack entries for routed destinations..."

    count=0
    flushed=""
    while IFS= read -r entry_raw || [ -n "$entry_raw" ]; do
        entry=$(echo "$entry_raw" | tr -d '\r')
        case "$entry" in
            ""|\#*) continue ;;
        esac

        if is_ipv4_entry "$entry"; then
            case " $flushed " in
                *" $entry "*) ;;
                *)
                    flushed="$flushed $entry"
                    conntrack -D -d "$entry" >/dev/null 2>&1 || true
                    conntrack -D -s "$entry" >/dev/null 2>&1 || true
                    ;;
            esac
            continue
        fi

        count=$((count + 1))
        if [ "${PRIME_MAX_DOMAINS:-0}" -gt 0 ] && [ "$count" -gt "$PRIME_MAX_DOMAINS" ]; then
            echo "--> Conntrack cleanup capped at ${PRIME_MAX_DOMAINS} domains."
            break
        fi

        for dns_server in 127.0.0.1 $(printf '%s\n' "$DNS_PRIME_SERVERS" | tr ',;' ' '); do
            dns_server=$(echo "$dns_server" | tr -d '\r')
            if [ -z "$dns_server" ]; then
                continue
            fi
            for ip in $(resolve_domain_ips "$entry" "$dns_server"); do
                case " $flushed " in
                    *" $ip "*) ;;
                    *)
                        flushed="$flushed $ip"
                        conntrack -D -d "$ip" >/dev/null 2>&1 || true
                        conntrack -D -s "$ip" >/dev/null 2>&1 || true
                        ;;
                esac
            done
        done
    done < "$DOMAIN_LIST"
}

# Main function to add rules and routes
add_routes() {
    echo "Configuring DNS and policy-based routing..."

    handle_ipv6_mode
    enable_ipv4_forwarding

    # Validate the dataplane before removing the previously working rules.
    # If the tunnel disappeared, safe direct mode removes stale marks instead
    # of sending LAN packets into a dead table.
    echo "--> Validating VPN route in table '$TABLE_NUM'..."
    if ! validate_vpn_route_or_safe_direct; then
        echo "Error: failed to create a usable VPN route for '$VPN_IFACE' in table '$TABLE_NUM'." >&2
        exit 1
    fi

    # 1. Cleanup old rules to ensure a fresh start
    cleanup_firewall

    # 2. Create or reset the shared ipset that will store our IPs
    echo "--> Preparing ipset '$IPSET_NAME'..."
    if ! ensure_shared_ipset; then
        echo "Error: failed to prepare ipset '$IPSET_NAME'." >&2
        exit 1
    fi

    # 3. Setup dnsmasq to populate the ipset automatically
    render_dnsmasq_config || exit 1

    if ! apply_dnsmasq_changes; then
        exit 1
    fi

    # 3c. Prime static IP ranges and current DNS answers.
    echo "--> Priming static IP routes..."
    prime_static_entries

    echo "--> Priming ipsets with current DNS answers..."
    prime_ipsets

    # 4. Re-check the policy route before any packets are marked.
    echo "--> Re-validating VPN route in table '$TABLE_NUM'..."
    if ! validate_vpn_route_or_safe_direct; then
        echo "Error: failed to create a usable VPN route for '$VPN_IFACE' in table '$TABLE_NUM'." >&2
        exit 1
    fi

    # 5. Create a rule to mark packets destined for our dynamic ipset
    echo "--> Ensuring iptables mangle rule..."
    if ! iptables -t mangle -C PREROUTING -i "$LAN_IFACE" -m set --match-set "$IPSET_NAME" dst -j MARK --set-mark "$FWMARK" >/dev/null 2>&1; then
        if ! iptables -t mangle -I PREROUTING -i "$LAN_IFACE" -m set --match-set "$IPSET_NAME" dst -j MARK --set-mark "$FWMARK"; then
            echo "Error: failed to insert the mangle mark rule." >&2
            exit 1
        fi
    fi

    # 6. Add forwarding and NAT rules to allow traffic into the tunnel
    forward_chain=$(resolve_forward_chain)
    echo "--> Ensuring firewall FORWARD and NAT rules..."
    if ! iptables -C "$forward_chain" -i "$LAN_IFACE" -o "$VPN_IFACE" -j ACCEPT >/dev/null 2>&1; then
        if ! iptables -I "$forward_chain" -i "$LAN_IFACE" -o "$VPN_IFACE" -j ACCEPT; then
            echo "Error: failed to insert the FORWARD rule into chain '$forward_chain'." >&2
            exit 1
        fi
    fi
    if [ "$VPN_MASQUERADE" = "1" ]; then
        if ! iptables -t nat -C POSTROUTING -o "$VPN_IFACE" -j MASQUERADE >/dev/null 2>&1; then
            if ! iptables -t nat -I POSTROUTING -o "$VPN_IFACE" -j MASQUERADE; then
                echo "Error: failed to insert the MASQUERADE rule for '$VPN_IFACE'." >&2
                exit 1
            fi
        fi
    fi

    # 6a. Force LAN clients to use router dnsmasq so ipset is populated.
    if ! ensure_dns_hijack_rules; then
        echo "Warning: failed to add DNS hijack rules; devices using external DNS may bypass domain routing." >&2
    fi

    # 6b. Clamp TCP MSS to avoid MTU blackholes through tunnels.
    if ! ensure_mss_clamp_rules; then
        echo "Warning: failed to add TCPMSS clamp rules; some HTTPS sites may stall on low-MTU tunnels." >&2
    fi

    # 6c. Insert domain stats accounting chain into FORWARD (both directions)
    if [ "$ENABLE_DOMAIN_STATS" = "1" ]; then
        echo "--> Ensuring domain stats accounting jump..."
        if ! ensure_domain_stats_jumps; then
            echo "Warning: failed to attach domain stats accounting chain; routing will continue." >&2
        fi
    fi

    # 7. Create a rule to use the new table for marked packets
    echo "--> Ensuring ip rule for marked packets..."
    while ip rule del fwmark "$FWMARK" table "$TABLE_NUM" >/dev/null 2>&1; do :; done
    if ! ip rule add fwmark "$FWMARK" table "$TABLE_NUM"; then
        echo "Error: failed to add ip rule for mark '$FWMARK' and table '$TABLE_NUM'." >&2
        exit 1
    fi

    # 8. Flush routing cache and, optionally, stale conntrack entries.
    ip route flush cache >/dev/null 2>&1 || true
    flush_conntrack_if_requested

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
    if ! validate_vpn_route_or_safe_direct; then
        echo "Error: failed to validate VPN route for sync." >&2
        exit 1
    fi

    cleanup_domain_stats
    cleanup_mss_clamp_rules
    if [ "$IPSET_FLUSH_ON_SYNC" = "1" ]; then
        ipset flush "$IPSET_NAME" >/dev/null 2>&1
    else
        echo "--> Soft sync: keeping existing ipset entries; stale dynamic entries will expire by timeout."
    fi

    render_dnsmasq_config || exit 1

    if ! apply_dnsmasq_changes; then
        exit 1
    fi

    echo "--> Priming static IP routes..."
    prime_static_entries

    echo "--> Priming ipsets with current DNS answers..."
    prime_ipsets

    if [ "$ENABLE_DOMAIN_STATS" = "1" ]; then
        echo "--> Restoring domain stats accounting jump..."
        if ! ensure_domain_stats_jumps; then
            echo "Warning: failed to attach domain stats accounting chain; routing will continue." >&2
        fi
    fi

    if ! ensure_dns_hijack_rules; then
        echo "Warning: failed to refresh DNS hijack rules." >&2
    fi
    if ! ensure_mss_clamp_rules; then
        echo "Warning: failed to refresh TCPMSS clamp rules." >&2
    fi

    ip route flush cache >/dev/null 2>&1 || true
    flush_conntrack_if_requested
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

    # 4. Remove dnsmasq config and reload only if it existed
    if [ -f "$DNSMASQ_CONFIG_FILE" ]; then
        echo "--> Removing dnsmasq config and reloading..."
        cp "$DNSMASQ_CONFIG_FILE" "$DNSMASQ_CONFIG_BACKUP" >/dev/null 2>&1 || true
        rm -f "$DNSMASQ_CONFIG_FILE"
        DNSMASQ_CONFIG_CHANGED=1
        if ! apply_dnsmasq_changes; then
            exit 1
        fi
    else
        echo "--> dnsmasq config already absent; reload skipped."
    fi

    # 5. Flush routing cache
    ip route flush cache >/dev/null 2>&1 || true

    echo "Cleanup complete."
}

# --- SCRIPT LOGIC ---
# Check for required utilities
for cmd in ip iptables ipset tr grep; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "Error: required utility '$cmd' not found."
        exit 1
    fi
done

setup_iptables_wait
if ! acquire_routing_lock; then
    exit 1
fi
trap release_routing_lock EXIT INT TERM

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
