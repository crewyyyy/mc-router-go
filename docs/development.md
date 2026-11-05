# Development

## Toolchain

The module declares Go 1.27.3. Newer compatible Go 1.x toolchains may also work. The source parser uses BurntSushi/toml for strict unknown-key detection and `golang.org/x/net/idna` for route normalization. Versions and checksums are committed in `go.mod`/`go.sum`.

```sh
go version
go mod verify
go test ./...
go test -race ./...
go vet ./...
make build
make check
```

`make integration` requires root and is only for the synthetic harness that first creates private network/mount/PID namespaces. Do not point it at a live Minecraft port; it starts its own fixtures. Ordinary `go test` and `go vet` need no root.

## Code boundaries

- `cmd/mc-router`: flags, process exit, logging and signal setup only.
- `internal/mcproto`: bounded initial Minecraft handshake parsing and hostname canonicalization.
- `internal/router`: immutable config/routing, synchronous admission, backend dials, opaque bidirectional TCP relay.
- `deploy/network.sh` and `network-guard.py`: optional root-managed Linux IPv4 namespace resources; never auto-run by the daemon.

The router must not parse game packets or trust hostname-derived IP/client metadata. Update tests and docs together when a config default, route normalization rule, timeout or Linux capability changes.

## Test expectations

Add regressions for malformed VarInts/frames, incomplete deadlines, byte-for-byte replay, unsupported target addresses, route normalization, per-IP/global/handshake caps, partial writes, blocked reverse-direction progress, half-close, idle timeout, high file descriptors and exact log/config behavior. Keep race-sensitive tests bounded and deterministic.

A TCP echo is not a login compatibility test. Label evidence accurately: unit test, loopback TCP, isolated network namespace, or real Minecraft client. Never report synthetic latency as TPS or player capacity.

## Release workflow

The project has no configured repository remote or initial Git commit yet. Before tagging/releasing, choose and verify the actual public repository owner/module path and confirm the release workflow runs from that repository. `.goreleaser.yaml` is the single artifact configuration; CI runs formatting, race tests, vet, Linux amd64/arm64 builds and TOML checks. The manually dispatched privileged netns job must remain separate from untrusted pull requests.
