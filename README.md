# mc-router

A small Minecraft Java TCP router that reads the initial hostname handshake and forwards the original byte stream to a configured backend. Derived in part from [itzg/mc-router](https://github.com/itzg/mc-router), commit `63534c550395a4b54ac4372fb8f2d108ee1bd213`, MIT. See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Features

- Static hostname routes and an optional default backend.
- Bounded handshake parsing; preserves the complete original handshake and any pipelined bytes.
- Absolute handshake deadline, separate pending/active connection caps, per-IP and global admission controls, bounded IP state.
- Independent bidirectional TCP relays, bounded writes, idle and half-close deadlines, graceful shutdown.
- Safe structured JSON logs; no public control API or player-identity parser.
- Optional **experimental** Linux IPv4 transparent source-IP mode. See [deploy/NETWORK.md](deploy/NETWORK.md) before enabling it.

This is TCP only. It does not authenticate players, implement RCON, parse game/login packets, or forward UDP voice chat. Vanilla clients before 1.7 are unsupported. IP forwarding does not fix `online-mode=false` or a disabled whitelist on a backend.

## Quick start

Prerequisite: a Linux/Unix host with a supported Go toolchain for source builds, or download a binary from a **future tagged GitHub Release**. There is no published release yet.

```sh
cp configs/config.example.toml config.toml
# Edit [routes] and [default] backend addresses.
go run ./cmd/mc-router --check-config --config ./config.toml
go run ./cmd/mc-router --config ./config.toml
```

The checker validates TOML, route keys, limits and upstream endpoints. It opens no listener, dials no backend and changes no network settings. Stop the foreground server with Ctrl-C. To build a standalone, CGO-free binary use `make build`; see [Installation](docs/installation.md) for binary/systemd/Docker instructions.

## Routing and configuration

A client connects to the configured listener, typically `:25565`. The router parses only the first Minecraft Java handshake, normalizes its hostname and chooses an exact route or the default. All bytes, including Forge metadata and data already pipelined after the handshake, are replayed unchanged.

Backends may be configured as literal IPs or administrator-selected DNS names in standard mode. DNS names are not accepted in transparent mode. The one canonical example is [`configs/config.example.toml`](configs/config.example.toml). See [configuration reference](docs/configuration.md).

## Transparent mode

Transparent mode preserves the IPv4 address that reaches the router by binding the upstream socket with Linux `IP_TRANSPARENT` and routing backend replies back through that socket. It needs an isolated backend network namespace, narrowly scoped nftables/policy routing setup, and a service-scoped `CAP_NET_RAW`. It is off by default, IPv4-only and marked **experimental**: the synthetic namespace test passed, but no real Forge/Horizon client login, loopback-backend topology or production migration has been tested. A backend behind an earlier VPN/NAT can only see the address the router actually receives.

## Tests

```sh
go test ./...
go test -race ./...
go vet ./...
make check
```

The privileged synthetic IP-preservation test is separate and runs entirely inside fresh network/mount/PID namespaces:

```sh
sudo bash tests/network_integration.sh "$(pwd)/build/mc-router"
```

It is not a Minecraft TPS benchmark or a production deployment test. Evidence and gaps are listed in [VALIDATION.md](VALIDATION.md).

## Installation, security, compatibility

- [Installation and rollback](docs/installation.md)
- [Architecture and limits](docs/architecture.md)
- [Security model](docs/security.md)
- [Transparent mode](docs/transparent-mode.md)
- [Troubleshooting](docs/troubleshooting.md)
- [Development](docs/development.md)

The systemd and Docker examples are templates and are not installed or enabled by building the project. Production cutover, backend restarts and firewall changes require an explicit operator decision.
