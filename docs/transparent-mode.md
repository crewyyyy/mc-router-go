# Experimental Linux transparent mode

Status: **experimental / opt-in / IPv4-only**. Standard mode does not need root or Linux packet-filter rules. Transparent mode is independent of Minecraft account authentication.

## Packet path

```text
external IPv4 client
    -> host :25565 (Go router reads Minecraft hostname handshake)
    -> IP_TRANSPARENT backend TCP socket with client's observed source IP
    -> backend in mcr-backends namespace via veth
    <- backend reply enters host over mcr-host
    <- nft socket-transparent match adds private fwmark
    <- fwmark policy rule + local route delivers packet to the matching router socket
    <- router forwards response on its original client TCP connection
```

These remain two TCP connections with different sequence/ack numbers. Merely setting `IP_TRANSPARENT`, binding a spoofed source address or accepting the backend TCP connect does not prove the return path. The isolated integration fixture verifies actual bidirectional TCP and the backend-observed peer address.

## Explicit limitations

- Only IPv4 is supported. Listener and backend must be IPv4, backend must be a literal, non-loopback IPv4 address, and the socket must support `IP_TRANSPARENT`. The process fails closed; it does not silently revert to a local source address.
- The currently tested topology places a backend in the isolated namespace behind the owned veth. The existing Forge `127.0.0.1` backend topology and an arbitrary `0.0.0.0` host listener have not been verified for transparent reply routing.
- No real external Minecraft status/login through Forge or Horizon has been tested. The synthetic client speaks only enough wire framing to test route selection and opaque TCP relay.
- The preserved address is the remote IP observed by the router, not any earlier address lost behind VPN, Xray, Hysteria, NAT or another TCP proxy. The client's source port is not preserved.
- The default network namespace has no backend Internet egress. Optional narrow TCP/DNS egress in `deploy/network.sh` is separate and not validated against mod authentication/update services. Existing Docker FORWARD policy may block it.
- Loopback control services (`RCON`, panel, supervisor API) are not automatically moved, mirrored or made reachable in the backend namespace.
- No performance or denial-of-service envelope is claimed from the bounded 100-synthetic-client smoke run.

## Safe isolated proof

Build a Linux binary and run:

```sh
make build
sudo bash tests/network_integration.sh "$(pwd)/build/mc-router"
```

The harness enters fresh mount, network and PID namespaces, verifies it left the invoking namespaces, and uses private `/run/netns`. It checks both a plain TCP baseline (backend sees the veth address) and transparent mode (backend sees the synthetic external client address), direct-ingress blocks, exact-byte relay, process capabilities, setup idempotence, refusal to delete a busy/unowned resource, cleanup/recreate and dotted interface names. It never binds host port `25565`, changes host firewall/routes/sysctls or contacts the real game servers.

Expected synthetic output includes:

```text
plain backend peer:       10.203.76.1
transparent backend peer: 198.51.100.2
```

The facts above do **not** meet the real-client gate for production readiness. See [VALIDATION.md](../VALIDATION.md).

## Operator setup

Do not start by editing the working VPS. First use the isolated proof and an expendable Ubuntu system with the same kernel/netfilter features. Before introducing this mode to a live game, the operator must plan the required backend restart/move and verify how Forge/Horizon control endpoints, voice chat, DNS, resource packs and outbound mod HTTPS will work across the namespace boundary.

The privileged network scripts are separate from the daemon and should be installed/owned by root. The service gets only `CAP_NET_RAW`; `CAP_NET_ADMIN`/root are needed only for `deploy/network.sh up|down`. Do not `setcap` a shared Python or Go compiler.

The setup has an ownership token, a root-only state directory tied to the current outer network namespace, fail-closed collision checks and limited rollback. A crash between kernel object creation and tagging intentionally requires manual inspection rather than automatic deletion/adoption. `down` refuses while processes are in the backend namespace. Follow [deploy/NETWORK.md](../deploy/NETWORK.md) for exact apply/status/revert and optional egress controls.
