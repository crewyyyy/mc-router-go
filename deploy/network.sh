#!/usr/bin/env bash
# IPv4-only transparent return-path staging. Never flushes unrelated resources.
set -Eeuo pipefail
umask 077
GUARD="$(dirname -- "$(readlink -f -- "$0")")/network-guard.py"
KEYS=(NETNS HOST_IF PEER_IF SUBNET HOST_IP BACKEND_IP NFT_TABLE MARK MASK ROUTE_TABLE RULE_PRIORITY ROUTE_PROTOCOL STATE_DIR EGRESS_IF TCP_PORTS TCP_DESTINATIONS DNS)
META=(TOKEN HOST_NS COMPLETE NS_CREATED VETH_CREATED NFT_CREATED ROUTE_CREATED RULE_CREATED)
declare -A EXPLICIT=()
NETNS=${MCR_NETNS-mcr-backends}
HOST_IF=${MCR_HOST_IF-mcr-host}
PEER_IF=${MCR_PEER_IF-mcr-peer}
SUBNET=${MCR_SUBNET-10.203.76.0/30}
HOST_IP=${MCR_HOST_IP-10.203.76.1}
BACKEND_IP=${MCR_BACKEND_IP-10.203.76.2}
NFT_TABLE=${MCR_NFT_TABLE-mc_router_go}
MARK=${MCR_MARK-0x100}
MASK=${MCR_MASK-0xff00}
ROUTE_TABLE=${MCR_ROUTE_TABLE-40076}
RULE_PRIORITY=${MCR_RULE_PRIORITY-1000}
ROUTE_PROTOCOL=${MCR_ROUTE_PROTOCOL-186}
STATE_DIR=${MCR_STATE_DIR-/run/mc-router-go-network}
EGRESS_IF=${MCR_EGRESS_IF-}
TCP_PORTS=${MCR_TCP_PORTS-443}
TCP_DESTINATIONS=${MCR_TCP_DESTINATIONS-0.0.0.0/0}
DNS=${MCR_DNS-}
for key in "${KEYS[@]}"; do
    envkey="MCR_$key"
    if [[ -v "$envkey" ]]; then EXPLICIT[$key]=1; fi
done
TOKEN= HOST_NS= COMPLETE=0 NS_CREATED=0 VETH_CREATED=0 NFT_CREATED=0 ROUTE_CREATED=0 RULE_CREATED=0
TRANSACTION=0

log() { printf 'network.sh: %s\n' "$*" >&2; }
die() { log "$*"; exit 1; }
guard() { python3 -I "$GUARD" "$@"; }
usage() {
    printf '%s\n' \
        'Usage: network.sh up|down|status|plan [options]' \
        '  --netns NAME            [mcr-backends]' \
        '  --host-if NAME          [mcr-host]' \
        '  --peer-if NAME          [mcr-peer]' \
        '  --subnet CIDR           [10.203.76.0/30; RFC1918 IPv4 /30 only]' \
        '  --host-ip IP            [10.203.76.1]' \
        '  --backend-ip IP         [10.203.76.2]' \
        '  --nft-table NAME        [mc_router_go; inet family]' \
        '  --mark INTEGER          [0x100]' \
        '  --mask INTEGER          [0xff00]' \
        '  --route-table INTEGER   [40076]' \
        '  --rule-priority INTEGER [1000]' \
        '  --route-protocol INTEGER [186; numeric ownership tag]' \
        '  --state-dir PATH        [/run/mc-router-go-network]' \
        '  --egress-if NAME        [disabled unless specified]' \
        '  --tcp-ports CSV         [443; optional egress destination ports]' \
        '  --tcp-destinations CSV  [0.0.0.0/0; optional egress IPv4 prefixes]' \
        '  --dns CSV               [no DNS egress; IPv4 resolver addresses]' \
        'Every option has an MCR_UPPER_SNAKE_CASE environment equivalent.' \
        'down/status load saved options from state; conflicting explicit options fail.' \
        'up/down require root. plan validates and prints only, with no network access.'
}

CMD=${1:-help}
(($# == 0)) || shift
case "$CMD" in up|down|status|plan) ;; help|-h|--help) usage; exit 0;; *) die "unknown command: $CMD";; esac
while (($#)); do
    [[ $1 != -h && $1 != --help ]] || { usage; exit 0; }
    (($# >= 2)) || die "option requires a value: $1"
    case "$1" in
        --netns) key=NETNS;; --host-if) key=HOST_IF;; --peer-if) key=PEER_IF;;
        --subnet) key=SUBNET;; --host-ip) key=HOST_IP;; --backend-ip) key=BACKEND_IP;;
        --nft-table) key=NFT_TABLE;; --mark) key=MARK;; --mask) key=MASK;;
        --route-table) key=ROUTE_TABLE;; --rule-priority) key=RULE_PRIORITY;;
        --route-protocol) key=ROUTE_PROTOCOL;; --state-dir) key=STATE_DIR;;
        --egress-if) key=EGRESS_IF;; --tcp-ports) key=TCP_PORTS;;
        --tcp-destinations) key=TCP_DESTINATIONS;; --dns) key=DNS;;
        *) die "unknown option: $1";;
    esac
    printf -v "$key" '%s' "$2"
    EXPLICIT[$key]=1
    shift 2
done

config_args() {
    CONFIG_ARGS=()
    for key in "${KEYS[@]}"; do CONFIG_ARGS+=("$key=${!key}"); done
}
validate() {
    local normalized key value
    config_args
    normalized=$(guard validate "${CONFIG_ARGS[@]}") || die 'invalid network configuration'
    while IFS='=' read -r key value; do printf -v "$key" '%s' "$value"; done <<< "$normalized"
}
validate
declare -A REQUEST=()
for key in "${KEYS[@]}"; do REQUEST[$key]=${!key}; done

# Printed nft program is also the one installed atomically, after collision checks.
render_nft() {
    local ports=${TCP_PORTS//,/, } destinations=${TCP_DESTINATIONS//,/, } resolvers=${DNS//,/, }
    printf 'add table inet %s { comment "%s"; }\n' "$NFT_TABLE" "$TOKEN"
    printf 'add chain inet %s divert { type filter hook prerouting priority -150; policy accept; }\n' "$NFT_TABLE"
    printf 'add rule inet %s divert iifname "%s" meta nfproto ipv6 counter drop\n' "$NFT_TABLE" "$HOST_IF"
    printf 'add rule inet %s divert iifname "%s" ip saddr != %s counter drop\n' "$NFT_TABLE" "$HOST_IF" "$BACKEND_IP"
    printf 'add rule inet %s divert iifname "%s" ip saddr %s meta l4proto tcp socket transparent 1 meta mark set (meta mark & %s) | %s counter accept\n' "$NFT_TABLE" "$HOST_IF" "$BACKEND_IP" "$INVERSE_MASK" "$MARK"
    printf 'add chain inet %s input_guard { type filter hook input priority -10; policy accept; }\n' "$NFT_TABLE"
    printf 'add rule inet %s input_guard iifname "%s" ip saddr %s meta l4proto tcp socket transparent 1 meta mark & %s == %s counter accept\n' "$NFT_TABLE" "$HOST_IF" "$BACKEND_IP" "$MASK" "$MARK"
    # Replies to ordinary host-originated TCP probes are safe too. This is not
    # permission for the namespace to initiate connections to host services.
    printf 'add rule inet %s input_guard iifname "%s" ip saddr %s ip daddr %s meta l4proto tcp ct direction reply ct state established counter accept\n' "$NFT_TABLE" "$HOST_IF" "$BACKEND_IP" "$HOST_IP"
    printf 'add rule inet %s input_guard iifname "%s" counter drop\n' "$NFT_TABLE" "$HOST_IF"
    printf 'add chain inet %s forward_guard { type filter hook forward priority -10; policy accept; }\n' "$NFT_TABLE"
    if [[ -n $EGRESS_IF ]]; then
        # Only connections explicitly admitted below receive our conntrack mark.
        printf 'add rule inet %s forward_guard iifname "%s" oifname "%s" ip daddr %s ct mark & %s == %s ct state established,related counter accept\n' "$NFT_TABLE" "$EGRESS_IF" "$HOST_IF" "$BACKEND_IP" "$MASK" "$MARK"
    fi
    printf 'add rule inet %s forward_guard oifname "%s" counter drop\n' "$NFT_TABLE" "$HOST_IF"
    if [[ -n $EGRESS_IF ]]; then
        printf 'add rule inet %s forward_guard iifname "%s" ip saddr != %s counter drop\n' "$NFT_TABLE" "$HOST_IF" "$BACKEND_IP"
        # Deny host/private/link-local/multicast destinations even with broad CIDRs.
        printf 'add rule inet %s forward_guard iifname "%s" ip daddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 224.0.0.0/4, 240.0.0.0/4 } counter drop\n' "$NFT_TABLE" "$HOST_IF"
        printf 'add rule inet %s forward_guard iifname "%s" oifname "%s" ip saddr %s ct mark & %s == %s ct state established,related counter accept\n' "$NFT_TABLE" "$HOST_IF" "$EGRESS_IF" "$BACKEND_IP" "$MASK" "$MARK"
        printf 'add rule inet %s forward_guard iifname "%s" oifname "%s" ip saddr %s ip daddr { %s } tcp dport { %s } tcp flags & (fin | syn | rst | ack) == syn ct state new ct mark set (ct mark & %s) | %s counter accept\n' "$NFT_TABLE" "$HOST_IF" "$EGRESS_IF" "$BACKEND_IP" "$destinations" "$ports" "$INVERSE_MASK" "$MARK"
        if [[ -n $DNS ]]; then
            printf 'add rule inet %s forward_guard iifname "%s" oifname "%s" ip saddr %s ip daddr { %s } udp dport 53 ct state new ct mark set (ct mark & %s) | %s counter accept\n' "$NFT_TABLE" "$HOST_IF" "$EGRESS_IF" "$BACKEND_IP" "$resolvers" "$INVERSE_MASK" "$MARK"
            printf 'add rule inet %s forward_guard iifname "%s" oifname "%s" ip saddr %s ip daddr { %s } tcp dport 53 tcp flags & (fin | syn | rst | ack) == syn ct state new ct mark set (ct mark & %s) | %s counter accept\n' "$NFT_TABLE" "$HOST_IF" "$EGRESS_IF" "$BACKEND_IP" "$resolvers" "$INVERSE_MASK" "$MARK"
        fi
    fi
    printf 'add rule inet %s forward_guard iifname "%s" counter drop\n' "$NFT_TABLE" "$HOST_IF"
    if [[ -n $EGRESS_IF ]]; then
        printf 'add chain inet %s egress_nat { type nat hook postrouting priority 100; policy accept; }\n' "$NFT_TABLE"
        printf 'add rule inet %s egress_nat iifname "%s" oifname "%s" ip saddr %s ct mark & %s == %s counter masquerade\n' "$NFT_TABLE" "$HOST_IF" "$EGRESS_IF" "$BACKEND_IP" "$MASK" "$MARK"
    fi
}
inverse_mask() { printf -v INVERSE_MASK '0x%x' "$(( (~MASK) & 0xffffffff ))"; }
interface_sysctls() {
    # Slash separators preserve literal dots in valid interface names.
    SYSCTL_ARGS=("net/ipv4/conf/$1/rp_filter=2" "net/ipv4/conf/$1/accept_redirects=0" "net/ipv4/conf/$1/send_redirects=0")
}
if [[ $CMD == plan ]]; then
    TOKEN=mc-router-go:00000000-0000-0000-0000-000000000000
    inverse_mask
    printf '# IPv4 only; no changes performed. %s (%s) -> %s (%s)\n' "$HOST_IF" "$HOST_IP" "$NETNS/$PEER_IF" "$BACKEND_IP"
    printf '# ip -4 rule add priority %s iif %s from %s/32 fwmark %s/%s table %s\n' "$RULE_PRIORITY" "$HOST_IF" "$BACKEND_IP" "$MARK" "$MASK" "$ROUTE_TABLE"
    printf '# ip -4 route add local default dev lo table %s proto %s\n' "$ROUTE_TABLE" "$ROUTE_PROTOCOL"
    interface_sysctls "$HOST_IF"
    printf '# host sysctl -q -w'; printf ' %q' "${SYSCTL_ARGS[@]}"; printf '\n'
    interface_sysctls "$PEER_IF"
    printf '# namespace sysctl -q -w'; printf ' %q' "${SYSCTL_ARGS[@]}"; printf '\n'
    printf '# disable IPv6 on new veths only: net/ipv6/conf/%s/disable_ipv6=1; net/ipv6/conf/%s/disable_ipv6=1\n' "$HOST_IF" "$PEER_IF"
    render_nft
    exit 0
fi

[[ $CMD != up && $CMD != down || $EUID == 0 ]] || die 'up/down require root'
for tool in ip nft python3 flock sysctl; do command -v "$tool" >/dev/null || die "missing command: $tool"; done
# A new state directory is the only filesystem mutation before preflight.
if [[ ! -e $STATE_DIR && ! -L $STATE_DIR && $CMD != up ]]; then
    log "down (no state at $STATE_DIR); unowned network resources were not inspected or changed"
    exit 0
fi
guard path "$STATE_DIR" "$([[ $CMD == up ]] && printf create || printf existing)"
# Never open a user-supplied lock symlink. The checked directory is root-only.
[[ ! -L $STATE_DIR/lock ]] || die 'lock is a symlink'
exec 9>"$STATE_DIR/lock"
flock -x 9
STATE_FILE=$STATE_DIR/state.json
if [[ -e $STATE_FILE || -L $STATE_FILE ]]; then
    loaded=$(guard load "$STATE_FILE")
    while IFS='=' read -r key value; do
        if [[ $key == STATE_DIR ]]; then
            [[ $value == "$STATE_DIR" ]] || die 'state directory does not match saved config'
        elif [[ -v REQUEST[$key] ]] && [[ $CMD == up || -v EXPLICIT[$key] ]]; then
            [[ ${REQUEST[$key]} == "$value" ]] || die "saved $key differs; use matching options or down first"
        fi
        printf -v "$key" '%s' "$value"
    done <<< "$loaded"
    [[ $HOST_NS == "$(readlink /proc/self/ns/net)" ]] || die 'state belongs to a different outer network namespace'
    HAS_STATE=1
else
    HAS_STATE=0
fi
inverse_mask

save() {
    local key
    config_args
    for key in "${META[@]}"; do CONFIG_ARGS+=("$key=${!key}"); done
    guard save "$STATE_FILE" "${CONFIG_ARGS[@]}"
}
inventory() {
    # Do not interpret a permission/parse error as absence.
    LINKS=$(ip -j link show) || return
    NS_LIST=$(ip netns list) || return
    TABLES=$(nft -j list tables) || return
    RULES=$(ip -j -N -4 rule show) || return
    ALL_ROUTES=$(ip -j -N -4 route show table all) || return
    ROUTES=$(guard routes-for "$ROUTE_TABLE" <<< "$ALL_ROUTES") || return
}
ns_exists() {
    local name rest
    while read -r name rest; do [[ $name != "$NETNS" ]] || return 0; done <<< "$NS_LIST"
    return 1
}
link_exists() { guard link-exists "$1" <<< "$LINKS"; }
table_exists() { guard nft-exists "$NFT_TABLE" <<< "$TABLES"; }
rule_exists() { guard rule-exists "$ROUTE_TABLE" "$RULE_PRIORITY" <<< "$RULES"; }
route_exists() { [[ $ROUTES != '[]' ]]; }
check_owned() {
    inventory || return
    if ns_exists; then
        [[ $NS_CREATED == 1 ]] || die 'unexpected namespace exists'
        ip -j -n "$NETNS" link show lo | guard alias "$TOKEN" || return
    fi
    if link_exists "$HOST_IF"; then
        [[ $VETH_CREATED == 1 ]] || die 'unexpected host interface exists'
        ip -j link show dev "$HOST_IF" | guard alias "$TOKEN" || return
    fi
    if link_exists "$PEER_IF"; then
        [[ $VETH_CREATED == 1 ]] || die 'unexpected peer interface exists'
        ip -j link show dev "$PEER_IF" | guard alias "$TOKEN" || return
    elif ns_exists && ip -n "$NETNS" link show dev "$PEER_IF" >/dev/null 2>&1; then
        [[ $VETH_CREATED == 1 ]] || die 'unexpected namespace peer exists'
        ip -j -n "$NETNS" link show dev "$PEER_IF" | guard alias "$TOKEN" || return
    fi
    if table_exists; then
        [[ $NFT_CREATED == 1 ]] || die 'unexpected nft table exists'
        nft -j list table inet "$NFT_TABLE" | guard nft-owned "$TOKEN" || return
    fi
    if route_exists; then
        [[ $ROUTE_CREATED == 1 ]] || die 'unexpected route table contents'
        guard route-owned "$ROUTE_PROTOCOL" <<< "$ROUTES" || return
    fi
    if rule_exists; then
        [[ $RULE_CREATED == 1 ]] || die 'unexpected policy rule/table reference'
        guard rule-owned "$ROUTE_TABLE" "$RULE_PRIORITY" "$MARK" "$MASK" "$HOST_IF" "$BACKEND_IP" <<< "$RULES" || return
    fi
}
check_ready() {
    [[ $COMPLETE == 1 ]] || die 'incomplete setup; run down before up'
    ns_exists && link_exists "$HOST_IF" && table_exists && route_exists && rule_exists || die 'owned setup is incomplete; run down before up'
    ip -j -4 addr show dev "$HOST_IF" | guard ready "$HOST_IP"
    ip -j -4 -n "$NETNS" addr show dev "$PEER_IF" | guard ready "$BACKEND_IP"
}
cleanup() {
    # Check every identity before the first deletion. Retain state on any error.
    check_owned || return
    if ns_exists; then
        local pids
        pids=$(ip netns pids "$NETNS") || return
        [[ -z $pids ]] || { log "namespace still has processes ($pids); stop them before down"; return 1; }
    fi
    # Quiesce the veth before removing firewall protection.
    if link_exists "$HOST_IF"; then ip link set dev "$HOST_IF" down || return; fi
    if rule_exists; then ip -4 rule del priority "$RULE_PRIORITY" iif "$HOST_IF" from "$BACKEND_IP/32" fwmark "$MARK/$MASK" table "$ROUTE_TABLE" || return; fi
    if route_exists; then ip -4 route del local default dev lo table "$ROUTE_TABLE" proto "$ROUTE_PROTOCOL" || return; fi
    if table_exists; then nft delete table inet "$NFT_TABLE" || return; fi
    if link_exists "$HOST_IF"; then
        ip link del dev "$HOST_IF" || return
    elif link_exists "$PEER_IF"; then
        ip link del dev "$PEER_IF" || return
    fi
    if ns_exists; then ip netns del "$NETNS" || return; fi
    rm -- "$STATE_FILE" || return
}
on_exit() {
    local result=$?
    trap - EXIT INT TERM
    if [[ $TRANSACTION == 1 && $result != 0 ]]; then
        log 'setup failed; rolling back verified owned resources'
        if ! cleanup; then log "rollback stopped safely; inspect $STATE_FILE before retrying down"; fi
    fi
    exit "$result"
}
trap on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

if [[ $CMD == down ]]; then
    if [[ $HAS_STATE == 0 ]]; then log 'down (no ownership state; no network changes)'; exit 0; fi
    cleanup
    log 'down; owned resources removed (state directory and lock retained)'
    exit 0
fi
if [[ $HAS_STATE == 1 ]]; then
    check_owned
    check_ready
    log "up: $NETNS $BACKEND_IP via $HOST_IF; ownership and required resources verified"
    exit 0
fi
[[ $CMD != status ]] || { log 'down (no ownership state)'; exit 0; }

# Refuse any preexisting resource, overlapping subnet, mark mask or table reference.
inventory
[[ ! -e /etc/netns/$NETNS && ! -L /etc/netns/$NETNS ]] || die 'preexisting /etc/netns profile; refusing inherited DNS/mount configuration'
if ns_exists || link_exists "$HOST_IF" || link_exists "$PEER_IF" || table_exists; then die 'preexisting unowned namespace/interface/nft table; refusing setup'; fi
guard rules-free "$MASK" "$ROUTE_TABLE" "$RULE_PRIORITY" <<< "$RULES"
guard routes-free "$SUBNET" "$ROUTE_TABLE" <<< "$ALL_ROUTES"
if [[ -n $EGRESS_IF ]]; then
    link_exists "$EGRESS_IF" || die 'egress interface does not exist'
    [[ $(sysctl -n net.ipv4.ip_forward) == 1 ]] || die 'optional egress needs existing host ip_forward=1; this script will not enable it'
fi
TOKEN="mc-router-go:$(python3 -I -c 'import uuid; print(uuid.uuid4())')"
HOST_NS=$(readlink /proc/self/ns/net)
save
TRANSACTION=1
# Journal intent first. Untagged resources after an abrupt crash are deliberately
# not auto-adopted or deleted. Namespace/veth creation has a small tagging window.
NS_CREATED=1; save
ip netns add "$NETNS"
ip -n "$NETNS" link set dev lo alias "$TOKEN"
VETH_CREATED=1; save
# Set aliases explicitly: some iproute2 versions silently lose aliases passed
# to the veth creation parser, even when the create command succeeds.
ip link add name "$HOST_IF" type veth peer name "$PEER_IF"
ip link set dev "$HOST_IF" alias "$TOKEN"
ip link set dev "$PEER_IF" alias "$TOKEN"
ip link set dev "$PEER_IF" netns "$NETNS"
ip addr add "$HOST_IP/30" dev "$HOST_IF"
ip -n "$NETNS" addr add "$BACKEND_IP/30" dev "$PEER_IF"
# Only new interfaces are changed, never host conf.all or conf.default.
# Loose rp_filter also works when the host's conf.all is strict (effective max).
interface_sysctls "$HOST_IF"
sysctl -q -w "${SYSCTL_ARGS[@]}" >/dev/null
interface_sysctls "$PEER_IF"
ip netns exec "$NETNS" sysctl -q -w "${SYSCTL_ARGS[@]}" >/dev/null
# The deployment is explicitly IPv4-only. IPv6 is disabled on these new veths.
if [[ -e /proc/sys/net/ipv6/conf/$HOST_IF/disable_ipv6 ]]; then sysctl -q -w "net/ipv6/conf/$HOST_IF/disable_ipv6=1" >/dev/null; fi
ip netns exec "$NETNS" sh -c 'if [ -e "/proc/sys/net/ipv6/conf/$1/disable_ipv6" ]; then printf 1 > "/proc/sys/net/ipv6/conf/$1/disable_ipv6"; fi' sh "$PEER_IF"
ROUTE_CREATED=1; save
ip -4 route add local default dev lo table "$ROUTE_TABLE" proto "$ROUTE_PROTOCOL"
RULE_CREATED=1; save
ip -4 rule add priority "$RULE_PRIORITY" iif "$HOST_IF" from "$BACKEND_IP/32" fwmark "$MARK/$MASK" table "$ROUTE_TABLE"
NFT_CREATED=1; save
render_nft | nft -f -
ip -n "$NETNS" link set dev lo up
ip link set dev "$HOST_IF" up
ip -n "$NETNS" link set dev "$PEER_IF" up
ip -n "$NETNS" route add default via "$HOST_IP" dev "$PEER_IF"
COMPLETE=1; save
check_owned
check_ready
TRANSACTION=0
log "up: $NETNS $BACKEND_IP; router stays in outer host namespace; backend services were not started or moved"
