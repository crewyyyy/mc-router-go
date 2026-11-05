# Security model and limitations

## Trust boundaries

The Minecraft client controls the initial handshake and all subsequent TCP bytes. The router parses only a bounded first frame, validates the hostname before route lookup, and uses that hostname as a key into a static administrator-controlled map. It never uses client input as a backend host or port. Standard-mode backend DNS names are part of the root-owned/operator-controlled config.

All configured backends are plain Minecraft TCP servers. The router does not authenticate Minecraft accounts, enforce a whitelist, inspect login identities, rewrite UUIDs, or implement Bungee/Velocity forwarding. In particular, `online-mode=false` allows name/identity spoofing unless a separately trusted Minecraft proxy authenticates players. IP transparency is not player authentication. Configure online-mode, whitelist and bans according to the server's actual topology.

## Listening and backend exposure

Bind the public listener only on interfaces intended to accept players. Restrict backend ports to the router/backend network: a second public route to `25567` or an accidentally public Forge port defeats the single-ingress policy. Check both host firewall and provider firewall. Standard mode alone does not hide a backend that is already bound to `0.0.0.0`.

Docker users should publish only router TCP `25565`; do not mount the Docker socket or use privileged mode. Backends reachable via Docker service DNS should be on the same user-defined network. `127.0.0.1` inside a container refers to that same container, not the host and not another container.

## Resource-exhaustion defenses

- Total accepted handlers, pending handshakes and per-source-IP handlers are capped independently.
- Rate buckets and the bounded per-IP state table limit attacker-created metadata.
- The first packet is size-checked before allocation and has an absolute deadline.
- Backend connect, each output chunk, session idleness, half-close drain and shutdown have separate deadlines.
- Relay buffers are fixed-size. Admission rejection is silent apart from an aggregate counter, avoiding per-packet log amplification.

Choose per-IP limits with shared NATs in mind. Aggressive rates or small limits may reject legitimate players behind a campus/mobile NAT. Defaults are configurable conservative starting values, not a tested DDoS envelope.

## Transparent mode

This opt-in Linux IPv4 feature spoofs the backend-facing TCP source IP. The router must be able to reach its configured backends with `IP_TRANSPARENT`, while the network policy returns backend reply packets to the router's socket. A misconfigured or missing route is a hard failure; it never falls back silently to ordinary proxying.

The router process should run unprivileged with only service-scoped `CAP_NET_RAW`. The separate, root-run network setup owns only its validated veth, namespace, nft table, policy rule and route. It refuses collisions, journals ownership, and does not flush firewall tables or change the host default route. Review [transparent mode](transparent-mode.md) and the operational [network deployment guide](../deploy/NETWORK.md) before use. No real Minecraft login, loopback-backend topology, IPv6-transparent topology or production migration has been validated; treat the mode as experimental.

Transparent mode preserves only the source address observed by the router. If an upstream VPN, TCP proxy, provider NAT or relay created a new connection first, the earlier client IP is already gone unless that earlier component has its own trusted forwarding method.

## Logs and privacy

The router emits structured JSON lifecycle and aggregate-stat logs. It does not parse or log player chat, game payloads, names or UUIDs. Client IP, hostname route and backend address are still operationally sensitive; set log access/retention appropriate for your environment. Debug logging can expose more handshake diagnostics and should not be left enabled without a reason.

There is no public admin API, metrics endpoint, pprof endpoint or configuration reload listener. Protect the TOML file and its parent directory from untrusted writers: a writable config can redirect backend dials, bind new listeners, or enable transparent sockets.

## Vulnerability reporting

No dedicated security contact or supported released version is established yet. Until one is published, do not send vulnerabilities in public issue reports; contact the repository maintainers through a private channel they publish when the project repository is created.
