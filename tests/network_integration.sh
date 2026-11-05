#!/usr/bin/env bash
# Run ONLY fixtures in new mount/network/PID namespaces. No production listeners,
# host routing, host nftables, game processes, or global sysctls are modified.
set -euo pipefail

self=$(realpath -- "${BASH_SOURCE[0]}")
project=$(dirname -- "$(dirname -- "$self")")

if [[ ${1:-} != --inside ]]; then
    [[ $EUID == 0 ]] || { printf '%s\n' 'Run with sudo; the script first isolates its own network/mount/PID namespaces.' >&2; exit 2; }
    binary=$(realpath -- "${1:-$project/build/mc-router}")
    [[ -x $binary ]] || { printf '%s\n' 'Build mc-router first.' >&2; exit 2; }
    original_net=$(readlink /proc/self/ns/net)
    original_mount=$(readlink /proc/self/ns/mnt)
    exec unshare --mount --net --pid --fork --mount-proc -- \
        bash "$self" --inside "$original_net" "$original_mount" "$binary"
fi

[[ $# == 4 && $EUID == 0 ]] || exit 2
[[ $(readlink /proc/self/ns/net) != "$2" ]] || { printf '%s\n' 'Refusing to test in original network namespace.' >&2; exit 2; }
[[ $(readlink /proc/self/ns/mnt) != "$3" ]] || { printf '%s\n' 'Refusing to test in original mount namespace.' >&2; exit 2; }
binary=$4
mount --make-rprivate /
mkdir -p /run/netns
mount -t tmpfs -o mode=755 tmpfs /run/netns
work=$(mktemp -d /tmp/mcr-net-test.XXXXXX)
# Root owns state/logs, but the router itself runs as an unprivileged UID with
# ONLY CAP_NET_RAW, mirroring the transparent systemd drop-in.
chmod 711 "$work"
router_uid=${SUDO_UID:-65534}
router_gid=${SUDO_GID:-65534}
if [[ $router_uid == 0 ]]; then router_uid=65534; router_gid=65534; fi
start_router() {
    setpriv --reuid="$router_uid" --regid="$router_gid" --clear-groups \
        --bounding-set=-all,+net_raw --inh-caps=+net_raw --ambient-caps=+net_raw \
        --no-new-privs "$binary" -config "$1" >"$2" 2>&1 &
    router_pid=$!
}
fixture="$project/tests/net_fixture.py"
network="$project/deploy/network.sh"
router_pid=''
backend_pid=''
cleanup() {
    local result=$?
    trap - EXIT
    if [[ -n $router_pid ]]; then kill -TERM "$router_pid" 2>/dev/null || true; wait "$router_pid" 2>/dev/null || true; fi
    if [[ -n $backend_pid ]]; then kill -TERM "$backend_pid" 2>/dev/null || true; wait "$backend_pid" 2>/dev/null || true; fi
    bash "$network" down --state-dir "$work/state" >"$work/network-down.log" 2>&1 || true
    ip netns del mcr-test-client 2>/dev/null || true
    printf 'Fixture logs: %s\n' "$work"
    exit "$result"
}
trap cleanup EXIT

ip link set lo up
# This is namespace-local, not the live host's sysctl. Enable forwarding so
# negative ingress tests prove nft isolation rather than merely ip_forward=0.
sysctl -q -w net.ipv4.ip_forward=1 >/dev/null
# Simulated Internet client: its address is NOT local to the proxy namespace.
ip netns add mcr-test-client
ip link add mcr-tclienth type veth peer name test-client-ns
ip link set test-client-ns netns mcr-test-client
ip addr add 198.51.100.1/30 dev mcr-tclienth
ip link set mcr-tclienth up
ip -n mcr-test-client link set lo up
ip -n mcr-test-client addr add 198.51.100.2/30 dev test-client-ns
ip -n mcr-test-client link set test-client-ns up
ip -n mcr-test-client route add default via 198.51.100.1

bash "$network" up --state-dir "$work/state" >"$work/network-up.log" 2>&1
bash "$network" status --state-dir "$work/state"
ip netns exec mcr-backends python3 -I "$fixture" backend \
    --address 10.203.76.2 --port 25567 --ready "$work/backend.ready" >"$work/backend.jsonl" 2>&1 &
backend_pid=$!
python3 -I "$fixture" wait --pid "$backend_pid" --ready "$work/backend.ready"

# Idempotence must not touch the running fixture; conflicting ownership and
# teardown of an occupied namespace must fail closed without breaking it.
bash "$network" up --state-dir "$work/state" >"$work/idempotent-up.log" 2>&1
if bash "$network" up --state-dir "$work/collision-state" >"$work/collision.log" 2>&1; then
    printf '%s\n' 'FAIL: adopted unowned preexisting network resources.' >&2; exit 1
fi
if bash "$network" down --state-dir "$work/state" >"$work/busy-down.log" 2>&1; then
    printf '%s\n' 'FAIL: removed namespace while backend was running.' >&2; exit 1
fi
bash "$network" status --state-dir "$work/state"

# These configurations exist only in the sandbox and do not touch production.
python3 -I - "$work" <<'PY'
import pathlib,sys
path=pathlib.Path(sys.argv[1])
for mode in ('plain','transparent'):
    lines=['[server]', 'listen = "198.51.100.1:25565"', '', '[routes]', '"horizon.example" = "10.203.76.2:25567"', '', '[default]', 'backend = "10.203.76.2:25567"', '', '[limits]', 'max_connections = 256', 'max_pending_handshakes = 128', 'max_connections_per_ip = 128', 'connection_rate = 1000.0', 'connection_burst = 512', 'ip_connection_rate = 1000.0', 'ip_connection_burst = 256', 'handshake_timeout = "2s"', 'dial_timeout = "2s"', 'write_timeout = "2s"', 'idle_timeout = "5s"', 'half_close_timeout = "200ms"', 'shutdown_timeout = "1s"', 'stats_interval = "1s"', '', '[transparent]', 'enabled = '+('true' if mode == 'transparent' else 'false'), '', '[logging]', 'level = "info"', '']
    (path/(mode+'.toml')).write_text('\n'.join(lines))
PY

chmod 644 "$work/plain.toml" "$work/transparent.toml"
start_router "$work/plain.toml" "$work/plain.jsonl"
python3 -I "$fixture" wait --pid "$router_pid" --listen 198.51.100.1 --port 25565
printf '%s\n' 'Isolation: direct client-to-backend and backend-initiated host connections must fail.'
ip netns exec mcr-test-client python3 -I - <<'PY'
import socket
with socket.socket() as c:
    c.settimeout(0.35)
    assert c.connect_ex(('10.203.76.2',25567)) != 0, 'public client bypassed proxy'
PY
ip netns exec mcr-backends python3 -I - <<'PY'
import socket
with socket.socket() as c:
    c.settimeout(0.35)
    assert c.connect_ex(('198.51.100.1',25565)) != 0, 'backend initiated unapproved host connection'
PY
printf '%s\n' 'Control: plain proxy must lose the client IP.'
ip netns exec mcr-test-client python3 -I "$fixture" client --expected-peer 10.203.76.1 --clients 1 --rounds 3
kill -TERM "$router_pid"; wait "$router_pid"; router_pid=''

start_router "$work/transparent.toml" "$work/transparent.jsonl"
python3 -I "$fixture" wait --pid "$router_pid" --listen 198.51.100.1 --port 25565
python3 -I - "$router_pid" <<'PY'
import json,pathlib,sys
fields=dict(line.split(':',1) for line in pathlib.Path('/proc/'+sys.argv[1]+'/status').read_text().splitlines() if ':' in line)
assert int(fields['Uid'].split()[1]) != 0, 'router unexpectedly runs as root'
assert int(fields['CapEff'].strip(),16) == 1<<13, 'router has capabilities other than CAP_NET_RAW'
assert int(fields['NoNewPrivs'].strip()) == 1
print(json.dumps({'event':'router_privileges_pass','uid':int(fields['Uid'].split()[1]),'effective_capability':'CAP_NET_RAW','no_new_privileges':True}))
PY
printf '%s\n' 'Transparent proxy must preserve non-local client IP through both TCP legs.'
ip netns exec mcr-test-client python3 -I "$fixture" client --expected-peer 198.51.100.2 --clients 1 --rounds 10
printf '%s\n' 'Bounded concurrency smoke: 100 simultaneous synthetic clients.'
ip netns exec mcr-test-client python3 -I "$fixture" client --expected-peer 198.51.100.2 --clients 100 --rounds 20
kill -TERM "$router_pid"; wait "$router_pid"; router_pid=''

nft list ruleset >"$work/nft-final.txt"
ip -4 rule show >"$work/rules-final.txt"
python3 -I - "$work" <<'PY'
import json,pathlib,sys
path=pathlib.Path(sys.argv[1])
records=[json.loads(line) for line in (path/'backend.jsonl').read_text().splitlines()]
ips={r['peer_ip'] for r in records if r.get('event')=='accepted'}
assert ips=={'10.203.76.1','198.51.100.2'}, ips
router=[json.loads(line) for line in (path/'transparent.jsonl').read_text().splitlines()]
connected=[r for r in router if r.get('msg')=='connected']
assert len(connected)==101,len(connected)
assert all(r['backend_source'].split(':')[0]=='198.51.100.2' for r in connected)
print(json.dumps({'event':'namespace_integration_pass','transparent_connections':len(connected),
                  'backend_observed_client_ip':'198.51.100.2','production_network_changed':False}))
PY
# Prove teardown and recreation, including the supported dotted-interface names.
kill -TERM "$backend_pid"; wait "$backend_pid" 2>/dev/null || true; backend_pid=''
bash "$network" down --state-dir "$work/state" >"$work/lifecycle-down.log" 2>&1
[[ ! -e $work/state/state.json ]] || { printf '%s\n' 'FAIL: ownership state survived teardown.' >&2; exit 1; }
if ip link show mcr-host >/dev/null 2>&1; then printf '%s\n' 'FAIL: owned veth survived teardown.' >&2; exit 1; fi
bash "$network" up --state-dir "$work/state" >"$work/lifecycle-reup.log" 2>&1
bash "$network" down --state-dir "$work/state" >"$work/lifecycle-redown.log" 2>&1
bash "$network" up --state-dir "$work/dot-state" --netns mcr-dot --host-if mcr.host --peer-if mcr.peer >"$work/dot-up.log" 2>&1
bash "$network" status --state-dir "$work/dot-state"
bash "$network" down --state-dir "$work/dot-state" >"$work/dot-down.log" 2>&1
printf '%s\n' '{"event":"network_lifecycle_pass","idempotence":true,"collision_refused":true,"busy_teardown_refused":true,"recreate":true,"dotted_interfaces":true}'
printf '%s\n' 'PASS: isolated transparent-proxy integration, ingress isolation, 100-client smoke and network lifecycle.'
