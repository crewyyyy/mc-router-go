# IPv4 backend network namespace staging

`network.sh` prepares networking only. It does not start the router, launch or move game servers, edit production configuration, install packages, load custom kernel modules, change DNS, or enable a host gateway. Do not run `up` on the production host until the isolated proof and an administrator's routing/firewall review are complete.

## Packet path

The Go router stays in the host network namespace. Its incoming TCP listener remains an ordinary host listener. For each backend connection, transparent mode sets `IP_TRANSPARENT` before binding the **observed client's IPv4 address** with an ephemeral source port. No PROXY protocol header is added, and no TUN device is needed.

The proposed staging topology is:

```text
client -> host router listener
          host IP_TRANSPARENT socket (source = client IP)
          -> mcr-host 10.203.76.1/30
          -> mcr-backends / mcr-peer 10.203.76.2/30 -> backend TCP listener

backend reply (source = 10.203.76.2, destination = client IP)
          -> namespace default gateway 10.203.76.1
          -> host mcr-host
          -> nft TCP socket transparent match
          -> masked packet mark 0x100/0xff00
          -> priority 1000 rule -> table 40076 local default dev lo
          -> transparent outgoing socket in the host router
```

The policy rule additionally requires `iif mcr-host` and `from 10.203.76.2/32`. It therefore cannot turn an unrelated marked Internet packet into local traffic. The nft rule preserves packet-mark bits outside the configured mask. There is no destination-port filter on the return rule: the TCP destination port is the router's ephemeral source port, not the backend's listening port.

This follows the `socket transparent` plus policy-local-routing mechanism described in the [Linux kernel transparent proxy documentation](https://docs.kernel.org/networking/tproxy.html). An incoming `tproxy to` listener redirection is not needed here: Minecraft clients explicitly connect to the router; the interception is for backend replies to its transparent outgoing sockets.

## Owned resources and safety limits

Defaults are a draft, not a claim that these names/addresses/mark bits are unused on every host.

| Resource | Default |
| --- | --- |
| Namespace | `mcr-backends` |
| Host / namespace veth | `mcr-host` / `mcr-peer` |
| RFC1918 IPv4 /30 | `10.203.76.0/30` |
| Host / backend addresses | `10.203.76.1` / `10.203.76.2` |
| Owned nft table | `inet mc_router_go` |
| Reserved packet and conntrack mark bits | `0x100/0xff00` |
| Local routing table / rule priority | `40076` / `1000` |
| Route protocol tag | numeric `186` within that dedicated table |
| Root-only ownership state | `/run/mc-router-go-network/state.json` |

Numeric protocol 186 is conventionally named `bgp` by iproute2; this script does not run BGP or modify other protocol-186 routes. Ownership checks use numeric output and the exact dedicated table/route.

- `up` and `down` require root plus the relevant namespace/netfilter capabilities. Dependencies: Bash, iproute2 with JSON/numeric output, nftables with `socket transparent` and table comments, Python 3.9+, util-linux `flock`, and `sysctl`.
- The script refuses existing namespace/interface/table names, route table references, occupied rule priorities, overlapping route prefixes, overlapping policy-rule mark masks, and inherited `/etc/netns/NAME` profiles. It does not adopt unknown resources, even if they look similar.
- The known Tailscale mask `0xff0000` is always forbidden. On the inspected host, Tailscale uses priorities 5210 through 5270, so the draft priority 1000 precedes those rules. The script does not change them or add a global `rt_tables` entry. Administrators must also reserve the chosen bits against marks used only inside other firewall rules; arbitrary nft expressions are not automatically audited.
- State is tied to the creating outer network namespace inode, with a random token on both veth aliases, the namespace loopback alias, and the nft table comment. A root-only lock serializes this instance. Atomic state writes journal resource creation intent.
- Repeated `up` with identical options verifies existing owned resources and succeeds; it does not repair drift. `down` and `status` load options from saved state; explicit conflicting options are rejected. Repeated `down` succeeds without touching unowned resources.
- `down` checks every present resource's identity before deleting anything. It refuses while processes remain in the backend namespace. Stop game servers/fixtures first. Then it takes the host veth down before removing the owned rule, route, nft table, veth pair, and namespace. The state directory and lock are retained.
- Normal partial failures trigger rollback of verified resources only. If rollback cannot prove ownership or delete a resource, state is retained. An abrupt kill/power failure between namespace/veth creation and the explicit ownership-alias commands can leave an untagged resource. A later `down` deliberately refuses it. Inspect and resolve that resource manually; there is no unsafe force/adopt option. Losing state also means losing automatic cleanup authority.
- State directory components must be real root-owned directories, with no symlinks; the leaf must be mode 0700. Writable ancestors are rejected except root-owned sticky directories such as `/tmp`. Use `/run`, not `/var/run` if the latter is a symlink. The state parent must already exist.
- Only sysctls on the newly created interfaces are changed. `rp_filter=2` uses loose reverse-path validation without disabling host `conf.all` validation. Host `all`, `default`, `ip_forward`, default routes, Docker chains, and existing nft tables are untouched. IPv6 is disabled on both new veth endpoints; IPv6 transparent routing is **not implemented**.

The owned nft table drops unsolicited forwarding into the backend namespace. It also drops unmatched namespace-to-host input, except established reply-direction TCP responses to connections the host initiated from its veth address. That narrow exception supports ordinary probes and the nontransparent integration control without allowing namespace-initiated host service access. Unmatched namespace forwarding is dropped by default, so a missing transparent socket cannot send a backend response to the Internet.

An nft `accept` in this table does not override a later drop in another base chain. Existing host INPUT rules can still block valid transparent socket delivery. Docker's existing FORWARD drop policy can still block optional outbound traffic. The script never bypasses either: an administrator must review and, if needed, separately authorize narrowly scoped exceptions. A privileged actor modifying the same resources concurrently is outside the ownership model.

## CLI

All runtime resource identifiers and addresses are configurable. Every CLI option also has an `MCR_` environment equivalent, for example `--host-if` is `MCR_HOST_IF` and `--route-table` is `MCR_ROUTE_TABLE`. CLI values take precedence. `--help` lists the full options.

Nonprivileged inspection of the exact proposed nft batch:

```sh
bash deploy/network.sh plan
python3 -I deploy/test_network.py -v
```

Example commands **for an already isolated outer network/mount namespace**:

```sh
bash deploy/network.sh up \
  --netns proof-backends --host-if proof-host --peer-if proof-peer \
  --subnet 10.203.77.0/30 --host-ip 10.203.77.1 --backend-ip 10.203.77.2 \
  --nft-table proof_router --mark 0x200 --mask 0xff00 \
  --route-table 40077 --rule-priority 1001 --route-protocol 242 \
  --state-dir /run/proof-router-state
bash deploy/network.sh status --state-dir /run/proof-router-state
# Stop any processes launched inside proof-backends first.
bash deploy/network.sh down --state-dir /run/proof-router-state
```

For `up`, repeat nondefault options on subsequent calls, or set matching environment variables. A configuration change requires `down` followed by `up`, not in-place mutation. `status` returns nonzero on incomplete state, ownership conflicts, or resource verification failure. An absent state is reported as down without adopting or asserting the absence of unowned network objects. Full live-state inspection generally requires root because ownership state is root-only and nftables reads require capabilities.

## Optional backend outbound HTTPS/DNS

Outbound access is **off by default**. For authentication/update flows, an administrator can stage:

```sh
bash deploy/network.sh plan --egress-if eth0 \
  --tcp-ports 443 --tcp-destinations 0.0.0.0/0 --dns 1.1.1.1,9.9.9.9
```

The same options on `up` enable narrowly scoped masquerading and forwarding only for new backend-initiated TCP SYNs to those destination ports/prefixes, plus UDP/TCP DNS to the listed resolver IPv4 addresses. Only admitted connections receive the reserved conntrack mark; replies must carry that mark and arrive through the selected egress interface. Existing host `ip_forward=1` is required; the script refuses rather than enabling it. Private, CGNAT, loopback, link-local, multicast and reserved high-range destinations remain blocked in this mode. HTTPS destinations are IP-prefix based, not a Microsoft/Mojang hostname allowlist; a broad prefix authorizes any public destination on the selected port.

`--dns` authorizes packets only. It does not edit host or namespace resolver files, create `/etc/netns` files, expose a DNS server, or open host services. The application's resolver must be configured separately. A host stub resolver at `127.0.0.53` is not reachable from the backend namespace. IPv4-only egress may need resolver/application settings to avoid relying on IPv6.

## Isolated proof, not live migration

The coordinator-owned harness runs a fake backend and synthetic clients only. Build the Go executable first, then explicitly authorize and run the harness when ready:

```sh
# Follow the project's build instructions first.
sudo bash tests/network_integration.sh /absolute/path/to/mc-router
```

The harness creates a new outer mount/network/PID namespace with `unshare --mount --net --pid --fork --mount-proc`, makes mounts private, and mounts private tmpfs on `/run/netns` **before** calling this script. This matters: `ip netns` names live in filesystem bind mounts, so network namespace isolation alone does not isolate `/run/netns` from the host. The fake backend binds `10.203.76.2:25567`; a separate synthetic client namespace uses `198.51.100.2`. The control demonstrates IP loss without transparency, then the transparent case must show that actual peer IP at the fake backend and complete bidirectional TCP traffic, including concurrent connections. Do not run a bare `up` on the host to approximate this proof.

The unprivileged tests validate CLI input, nft rendering, dotted-interface sysctl paths, collision detection and ownership matching. They do not prove kernel socket routing or nft feature availability. The coordinator ran the isolated harness successfully during implementation: the plain control backend saw `10.203.76.1`, transparent connections saw the actual synthetic client `198.51.100.2`, and 100 simultaneous clients completed 2,000 exact-wire roundtrips (101 transparent connections including the initial single-client case). This is fixture-only evidence, not a production load result, outbound-NAT validation, or a migration of an existing game server. A final rerun is still appropriate after local configuration or tooling changes.

Moving an existing Forge/Horizon server into this namespace is a separate operational migration requiring a stop/restart. An existing process cannot be transparently relocated by this script. Listening addresses, backend route destinations, filesystem/mount access, panel launch commands and service ordering need review. Loopback is namespace-local: a control panel, RCON endpoint or plugin previously using host `localhost` is no longer sharing the same loopback. Do not expose RCON/panel ports publicly to compensate. Game authentication, DNS and outbound dependencies require the separate decisions above. No production services, firewall, routes, DNS or backends are changed by writing or inspecting this tooling.
