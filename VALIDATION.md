# Validation record

This document separates automated/unit evidence, synthetic TCP evidence, namespace evidence and unperformed real-server tests. It does not claim a production migration or stable release.

## Toolchain and source snapshot

- Go toolchain tested: Go 1.27.3, Linux amd64.
- Reference implementation: itzg/mc-router commit `63534c550395a4b54ac4372fb8f2d108ee1bd213`.
- Runtime modules: BurntSushi/toml v1.6.0, golang.org/x/net v0.62.0, golang.org/x/text v0.43.0; exact sums in `go.sum`.
- A local Git repository and initial source commit now exist; no remote, tag or release has been published yet. This is not a released archive.

## Automated checks

| Check | Recorded result |
|---|---|
| `go test -race -count=1 -timeout=90s ./...` | Pass; includes CLI tests, malformed TOML, parser/admission/relay regressions |
| Internal race suite repeated five times | Pass during the earlier regression-test pass |
| `go vet ./...` | Pass |
| `go mod verify` | All modules verified |
| Handshake fuzz test, 2 workers, about 5s | Pass; about 289,000 generated executions in one recorded run; not exhaustive |
| `govulncheck` v1.9.0 | No known vulnerabilities found by the database available at the run time |
| `python3 -I deploy/test_network.py -v` | 14 tests pass |
| `bash -n deploy/network.sh tests/network_integration.sh` | Pass |
| `systemd-analyze verify` service templates | Pass; this does not mean the units were installed or started |
| Final `go test -race -count=1 -timeout=90s ./...` | Pass on Go 1.27.3, Linux amd64 |
| `gofmt` check, `go vet ./...`, `go mod tidy -diff`, `go mod verify` | Pass |
| Standard, Docker DNS, transparent TOML `--check-config` | Pass with the final locally built binary; check-config opens no sockets |
| GoReleaser config/release workflow | Not validated; GoReleaser is not installed and no release was published |
| `docker compose config --quiet` | Pass; this validates syntax, not Compose deployment or backend reachability |
| Docker image build and runtime smoke | Pass; local nonroot image, `--help`, mounted config check, `--read-only`, no capabilities, no-new-privileges |
| `systemd-analyze verify` | Pass in a temporary root with staged executables; units were not installed or started |

Machine-specific command logs and any local binary hash are intentionally not checked in.

## Regression coverage

- Unbounded thread/session admission → independent active/pending/IP limits applied before handler creation.
- Per-read timeout slowloris → absolute accepted-at handshake deadline.
- `select` FD >=1024 failure → Go runtime network poller, high-FD Linux test.
- Blocking one direction → separate relay pumps, fixed buffer and bounded write deadline.
- Permanent half-close → `CloseWrite` plus bounded reverse drain.
- UTF-8 bytes confused with Unicode characters → independent byte/UTF-16 code-unit checks and IDNA normalization.
- Control-character log injection → malformed control/whitespace is rejected, protocol strings are structured JSON fields.
- Additional edge cases: capped TOML size without artificial-EOF bypass, strict unknown TOML keys/types, rate/IP-table TTL, pending-handshake capacity, configured handshake frame limit enforced before body read/allocation, exact replay of optimistic pipelined bytes.

## Isolated network-namespace evidence

A coordinator-run privileged harness used disposable network/mount/PID namespaces, fake TCP client/backend and reserved documentation IP ranges; it did not touch the production listener, host firewall, host routes, live services or DNS.

- Baseline non-transparent TCP: backend peer was the veth/router address `10.203.76.1`.
- Transparent TCP: backend peer was synthetic client `198.51.100.2`; response reached that client through the router.
- Final TOML-fixture rerun: 101 transparent connections total (single test + 100 simultaneous), 2,000 concurrent-phase exact-payload round-trips, no relay errors in the fixture.
- In that run, the 100-client synthetic median RTT was 1.732 ms, p95 7.668 ms, and elapsed time 0.393 s. The single-client transparent case measured median 0.852 ms, p95 1.155 ms. This includes Python echo clients and network namespaces; it is **not Minecraft TPS, gameplay latency, throughput capacity or a DDoS limit**.
- The router ran as non-root UID 1000 with only effective `CAP_NET_RAW` and `NoNewPrivs=1` inside the fixture.
- Direct external-client-to-backend access and backend-initiated new connections to the host were rejected inside the test namespace.
- Also checked idempotent setup, refusal to adopt a colliding unowned resource, refusal to tear down a busy backend namespace, cleanup/recreate and dotted veth names.

This was a synthetic client and echo backend. No Forge/Horizon process or real external Minecraft client participated.

## Remaining blockers / unverified

- No real Minecraft status ping or game login to either existing Forge or Horizon server.
- No external-client validation of backend-observed addresses in the actual service topology.
- No test of the current loopback Forge topology with transparent routing; transparent config intentionally rejects loopback backends.
- No 10/50/200/500 multi-level performance/RSS/FD benchmark or capacity/DDoS statement.
- No optional namespace TCP/DNS egress test against real game auth/update/mod services.
- No IPv6 transparent mode.
- No clean-VM install/reboot/update/rollback/uninstall, GitHub CI run, GitHub release, checksum verification of published artifacts, or public vulnerability intake.
- No production config/service/firewall/DNS/backend changes.

Transparent mode remains **experimental** until appropriate real Minecraft and operator-topology tests close the gaps. Ordinary TCP standard mode is not gated on those tests.
