# Troubleshooting

## `--check-config` reports an unknown key or type

Compare the TOML keys with [configuration reference](configuration.md). Keys are strict. Durations are quoted (`"5s"`, not integer nanoseconds). Backend values use `host:port`; IPv6 literals require brackets (`[2001:db8::10]:25565`). Check route keys for duplicates after lowercasing/IDNA and for Forge/query metadata, which belongs in the client handshake, not config.

## `listen: bind: address already in use`

The selected listener port already has a process. Inspect before stopping anything:

```sh
sudo ss -ltnp 'sport = :25565'
systemctl status mc-router.service --no-pager
```

Do not stop the current router as part of an ordinary test. A second test instance can use an unused port and a test config. Production cutover is a separate approved operation.

## Router starts, but all backends fail

Check that the configured address is reachable **from the router's own network namespace/container** and uses the game TCP port. For a local process:

```sh
ss -ltnp
nc -vz 127.0.0.1 25568
```

For Docker, `127.0.0.1` means the router container. Attach both services to the same user-defined network and use the backend service DNS name from that network. Standard-mode configuration permits admin-selected DNS targets; the handshake hostname cannot create one.

Inspect structured service logs:

```sh
journalctl -u mc-router.service -f
```

Do not turn on raw packet/game payload logging; this router doesn't need it to select a route.

## Route went to default instead of named backend

Observe the `hostname` and `backend` fields in the `connected` JSON event. The match is normalized hostname exact match, including trailing-root-dot stripping, case folding and IDNA. Forge NUL metadata and the supported `_o` query form are normalized only for matching; wire bytes remain unchanged. Unknown routes use `[default].backend` if present, otherwise close.

## Backend sees router's IP instead of player's

This is expected in standard mode. It preserves TCP without sending a PROXY header, but backend `RemoteAddress` is the router address. The optional transparent mode requires the tested Linux backend namespace and return route; `--check-config` cannot check kernel routing.

Useful diagnostics on the test host:

```sh
sudo bash deploy/network.sh status
sudo ip -4 rule show
sudo ip -4 route show table 40076
sudo nft list table inet mc_router_go
sudo ss -ntp
```

Do not apply generic TPROXY snippets or change a host default route to fix a namespace return path. Use the safe isolated proof first. If direct backend SYNs, Docker/NAT rules or Tailscale marks differ, stop and inspect rather than clearing nftables/routing state.

## Backend namespace `up` fails or `down` refuses

`network.sh` deliberately refuses to adopt an existing namespace/table/interface or delete a resource whose ownership cannot be proven. `down` also refuses while namespace PIDs exist. Inspect the state file and actual `ip`/`nft` inventory before deciding how to resolve it; do not remove random state files or flush firewall tables. An abrupt crash during the small create-before-tag interval can leave an untagged object; that requires manual identification/removal.

## Shutdown / reconnect issues

`--help` describes the current CLI. Session counters are periodically emitted as aggregate JSON. On `SIGTERM`, accepting stops, connections get the configured drain period, then remaining sockets are cancelled. Increase `limits.shutdown_timeout` only if the service manager `TimeoutStopSec` is increased too. A slow/blocked client is bounded by write/idle/half-close timeouts.

The router currently has no config reload; restart the service after an approved config change. Always run `--check-config` before restarting.
