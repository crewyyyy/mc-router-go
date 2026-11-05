# Contributing

The project is currently a local source tree without a configured Git remote. Once a public repository exists, use focused pull requests and include enough information to reproduce tests.

## Development checks

```sh
gofmt -w ./cmd ./internal
go test ./...
go test -race ./...
go vet ./...
go mod verify
make check
```

A source/config/protocol change should include focused regression coverage and updated docs/examples. Do not commit private hostnames/IPs, absolute home paths, configs, tokens, production logs, or Minecraft world/player data. Keep standard-mode tests unprivileged; privileged namespace tests stay separately invoked and never run against live services.

## Design constraints

- Preserve handshake bytes; route only by a canonical hostname from the first frame.
- Do not add player authentication, game-packet parsing, admin APIs or dynamic configuration without a separate reviewed requirement.
- Transparent networking is opt-in, Linux IPv4-only, and experimental until its outstanding production-compatible tests are documented.
- Do not silently fall back from transparent to standard source addressing.
- Keep network setup out of router startup. Cleanup may remove only resources whose ownership is proven.

## Pull request checklist

- [ ] `gofmt`, `go vet`, `go test`, and `go test -race` pass.
- [ ] Regression test covers the changed behavior/failure mode.
- [ ] Config examples and documentation match the actual parser/CLI.
- [ ] Privileges and newly opened ports are unchanged or explicitly justified.
- [ ] Evidence distinguishes unit, synthetic TCP, namespace, and real Minecraft tests.
- [ ] No claims of TPS/capacity from synthetic echo timings.
