# Architecture

The executable is a TCP router, not a Minecraft proxy that terminates player sessions.

```text
client TCP :25565 -> bounded handshake parser -> hostname route -> TCP dial -> backend
                                                              <- full-duplex relay ->
```

## Request lifecycle

1. `Serve` accepts a TCP connection. Synchronously, before the handler goroutine is spawned, the admission controller enforces total sessions, unfinished handshakes, per-IP sessions, global/per-IP token buckets and a bounded IP-state table.
2. An absolute read deadline and configurable frame bound are set. The `mcproto` package parses only the first Java handshake. It validates the VarInts, frame/address limits, UTF-8/UTF-16 lengths, next-state value and hostname normalization.
3. The route is selected once from an immutable table. DNS resolution of a configured standard-mode backend may occur at dial time; no client input is passed to a resolver or dialer.
4. The upstream TCP connection is established with its own timeout. The captured handshake bytes are written exactly once, followed by all pipelined bytes still in the socket.
5. Two independent pumps relay client→backend and backend→client. Each has a fixed 32 KiB buffer and a bounded output-chunk deadline. One blocked write doesn't stop reads and writes in the opposite direction.
6. EOF propagates through TCP `CloseWrite`. The reverse half-close drain, idle session and graceful shutdown all have separate deadlines. Handler completion closes both sockets and releases the admission lease.

## Concurrency and bounds

Go's `net` runtime poller uses epoll on Linux; the implementation does not build its own poll loop and has no `FD_SETSIZE` ceiling. Goroutines are per admitted connection, but no more than `max_connections` can exist. Of these, no more than `max_pending_handshakes` may still be parsing the initial frame. Every connection enters the capped set before a goroutine starts.

Handshakes allocate at most `handshake_max_bytes`; relay memory is fixed per direction. Per-IP state is capped by `max_ip_entries`; expired records are removed on demand. Rejected connections don't emit a per-connection log record. These are implementation bounds, not a promise of a particular player/TPS capacity.

No `sync.Pool`, custom epoll, or lock-free route reload is used. Configuration is validated before opening the listener and copied into immutable route state. Dynamic route reload, metrics endpoints, plugins, autoscaling and client login parsing are out of scope.

## Hostname and bytes

Only the first handshake is interpreted. Forge NUL metadata, legacy routing suffixes, DNS root dots, case and IDNA are handled for route matching. The original frame is stored separately and forwarded without rewriting; subsequent status/login/game bytes are opaque.

Unicode is checked against the handshake string's UTF-16 code-unit bound separately from its VarInt UTF-8 byte count. Malformed, control-bearing, oversized or invalid-state handshakes are closed. Logs contain normalized host/route values through the structured JSON encoder; game packets, usernames and chat are never decoded.

## Standard and transparent dials

Standard mode uses the ordinary Go TCP dialer. The backend sees the router/namespace address as its peer. Transparent mode is a Linux-only optional dialer that binds the original observed IPv4 source IP using `IP_TRANSPARENT`; its reply path depends on the separately staged `deploy/network.sh` namespace and policy routing. The mode is marked experimental until real Minecraft status/login and the required alternative backend topologies are independently tested.

## Evidence

Unit/race/parser tests and namespace synthetic TCP evidence are summarized in [VALIDATION.md](../VALIDATION.md). A loopback echo fixture is not a Minecraft client. The current validation does not establish Forge/Horizon login compatibility, gameplay load/TPS, production return routing, or external VPN/NAT original-IP recovery.
