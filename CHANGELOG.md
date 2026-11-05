# Changelog

This project has no tagged releases yet. Changes below are the implementation snapshot, not a released version.

## Unreleased

- Add Go Minecraft Java TCP hostname router with bounded handshake parser and exact byte replay.
- Add static routes, optional default backend and strict TOML configuration.
- Add independent pending-handshake/active-session caps, token-bucket admission and bounded per-IP state.
- Add bounded full-duplex relay, deadlines, SIGTERM shutdown and structured logs.
- Add experimental Linux IPv4 transparent source-IP path and separately managed namespace tooling.
- Add isolated namespace integration harness, tests, and future binary/OCI/systemd packaging templates.

## Compatibility and release status

- No GitHub Release/binary artifacts or stable compatibility guarantees exist yet.
- Real Minecraft Forge/Horizon login and production migration remain unverified.
