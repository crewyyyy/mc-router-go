#!/usr/bin/env python3
"""Private validation/state helper for network.sh. No network mutation."""
import ipaddress as ip
import json
import os
import re
import stat
import sys
import tempfile

KEYS = (
    "NETNS", "HOST_IF", "PEER_IF", "SUBNET", "HOST_IP", "BACKEND_IP",
    "NFT_TABLE", "MARK", "MASK", "ROUTE_TABLE", "RULE_PRIORITY", "ROUTE_PROTOCOL",
    "STATE_DIR", "EGRESS_IF", "TCP_PORTS", "TCP_DESTINATIONS", "DNS",
)
META = ("TOKEN", "HOST_NS", "COMPLETE", "NS_CREATED", "VETH_CREATED",
        "NFT_CREATED", "ROUTE_CREATED", "RULE_CREATED")


def require(ok, message):
    if not ok:
        raise ValueError(message)


def number(value):
    require(bool(re.fullmatch(r"(?:0x[0-9a-fA-F]+|[0-9]+)", value)), "invalid integer")
    return int(value, 16 if value.startswith("0x") else 10)


def validate(c):
    require(set(c) == set(KEYS), "unknown or missing configuration field")
    for key in ("NETNS",):
        require(bool(re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]{0,47}", c[key])), f"invalid {key}")
    for key in ("HOST_IF", "PEER_IF", "EGRESS_IF"):
        if key == "EGRESS_IF" and not c[key]:
            continue
        require(bool(re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]{0,14}", c[key])) and c[key] != "lo", f"invalid {key}")
    require(c["HOST_IF"] != c["PEER_IF"], "veth names must differ")
    require(c["EGRESS_IF"] not in (c["HOST_IF"], c["PEER_IF"]), "egress cannot be a veth endpoint")
    require(bool(re.fullmatch(r"[a-zA-Z_][a-zA-Z0-9_]{0,47}", c["NFT_TABLE"])), "invalid NFT_TABLE")
    network = ip.IPv4Network(c["SUBNET"], strict=True)
    private = [ip.IPv4Network(x) for x in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16")]
    require(network.prefixlen == 30 and any(network.subnet_of(x) for x in private), "SUBNET must be an RFC1918 IPv4 /30")
    host, backend = ip.IPv4Address(c["HOST_IP"]), ip.IPv4Address(c["BACKEND_IP"])
    require(host != backend and host in list(network.hosts()) and backend in list(network.hosts()), "HOST_IP and BACKEND_IP must be the two different /30 host addresses")
    for key in ("SUBNET", "HOST_IP", "BACKEND_IP"):
        require(str({"SUBNET": network, "HOST_IP": host, "BACKEND_IP": backend}[key]) == c[key], f"noncanonical {key}")
    mark, mask = number(c["MARK"]), number(c["MASK"])
    require(0 < mark <= 0xffffffff and 0 < mask <= 0xffffffff and mark & ~mask == 0, "mark must be nonzero and fit its nonzero 32-bit mask")
    require(mask & 0xff0000 == 0, "mask overlaps reserved Tailscale mark bits 0xff0000")
    c["MARK"], c["MASK"] = hex(mark), hex(mask)
    table, priority, protocol = (number(c[k]) for k in ("ROUTE_TABLE", "RULE_PRIORITY", "ROUTE_PROTOCOL"))
    require(1 <= table < 2**31 and table not in (52, 253, 254, 255), "reserved or invalid route table")
    require(1 <= priority < 32766, "rule priority must be 1..32765")
    require(186 <= protocol <= 252, "route protocol must be in supported numeric tag range 186..252")
    for key, value in (("ROUTE_TABLE", table), ("RULE_PRIORITY", priority), ("ROUTE_PROTOCOL", protocol)):
        c[key] = str(value)
    ports = c["TCP_PORTS"].split(",")
    require(1 <= len(ports) <= 32 and all(re.fullmatch(r"[0-9]{1,5}", p) and 1 <= int(p) <= 65535 for p in ports), "invalid TCP_PORTS list")
    c["TCP_PORTS"] = ",".join(str(p) for p in sorted(set(map(int, ports))))
    destinations = c["TCP_DESTINATIONS"].split(",")
    require(1 <= len(destinations) <= 32, "invalid TCP_DESTINATIONS list")
    c["TCP_DESTINATIONS"] = ",".join(str(ip.IPv4Network(x, strict=True)) for x in destinations)
    dns = c["DNS"].split(",") if c["DNS"] else []
    require(len(dns) <= 8, "at most eight DNS resolver addresses")
    c["DNS"] = ",".join(str(ip.IPv4Address(x)) for x in dns)
    require(not dns or c["EGRESS_IF"], "DNS egress requires EGRESS_IF")
    path = c["STATE_DIR"]
    require(path.startswith("/") and path != "/" and os.path.normpath(path) == path and not any(x in path for x in "\n\r\t="), "STATE_DIR must be a normalized absolute path")
    return c


def pairs(args):
    out = {}
    for arg in args:
        k, v = arg.split("=", 1)
        require(k not in out, f"duplicate field {k}")
        out[k] = v
    return out


def secure_path(path, create=False):
    """Never follow state path symlinks, including in parent components."""
    current = "/"
    parts = path.strip("/").split("/")
    for i, part in enumerate(parts):
        current = os.path.join(current, part)
        try:
            s = os.lstat(current)
        except FileNotFoundError:
            require(create and i == len(parts) - 1, f"missing state parent {current}")
            os.mkdir(current, 0o700)
            s = os.lstat(current)
        require(stat.S_ISDIR(s.st_mode) and not stat.S_ISLNK(s.st_mode), f"unsafe state path {current}")
        require(s.st_uid == 0, f"state path must be root-owned: {current}")
        if i == len(parts) - 1:
            require(stat.S_IMODE(s.st_mode) == 0o700, "state directory must have mode 0700")
        else:
            require(not s.st_mode & 0o022 or (s.st_mode & stat.S_ISVTX and s.st_uid == 0), f"writable state parent {current}")


def load(path):
    secure_path(os.path.dirname(path))
    s = os.lstat(path)
    require(stat.S_ISREG(s.st_mode) and s.st_uid == 0 and stat.S_IMODE(s.st_mode) == 0o600, "state file must be a root-owned regular file with mode 0600")
    with open(path, encoding="utf8") as f:
        data = json.load(f)
    require(set(data) == {"version", "config", "meta"} and data["version"] == 1, "unknown state format")
    data["config"] = validate(data["config"])
    m = data["meta"]
    require(set(m) == set(META), "unknown or missing state metadata")
    require(bool(re.fullmatch(r"mc-router-go:[0-9a-f-]{36}", m["TOKEN"])), "invalid ownership token")
    require(bool(re.fullmatch(r"net:\[[0-9]+\]", m["HOST_NS"])), "invalid host netns identity")
    require(all(m[k] in ("0", "1") for k in META if k not in ("TOKEN", "HOST_NS")), "invalid state flags")
    return data


def table_id(value):
    # ip JSON can use names for builtin routing tables/protocols.
    value = {"local": 255, "main": 254, "default": 253, "unspec": 0}.get(str(value), value)
    return int(value) if str(value).isdigit() else value


def main():
    action, args = sys.argv[1], sys.argv[2:]
    if action == "validate":
        for k, v in validate(pairs(args)).items():
            print(f"{k}={v}")
    elif action == "path":
        secure_path(args[0], args[1] == "create")
    elif action == "load":
        data = load(args[0])
        for k, v in (data["config"] | data["meta"]).items():
            print(f"{k}={v}")
    elif action == "save":
        path, rest = args[0], pairs(args[1:])
        config = validate({k: rest.pop(k) for k in KEYS})
        require(set(rest) == set(META), "invalid state metadata")
        secure_path(os.path.dirname(path))
        fd, tmp = tempfile.mkstemp(prefix=".state-", dir=os.path.dirname(path))
        try:
            with os.fdopen(fd, "w", encoding="utf8") as f:
                json.dump({"version": 1, "config": config, "meta": rest}, f, indent=2)
                f.write("\n")
                f.flush()
                os.fsync(f.fileno())
            os.replace(tmp, path)
        finally:
            if os.path.exists(tmp):
                os.unlink(tmp)
    elif action == "alias":
        data = json.load(sys.stdin)
        require(len(data) == 1 and data[0].get("ifalias") == args[0], "interface/namespace ownership alias mismatch")
    elif action == "nft-owned":
        data = json.load(sys.stdin)
        tables = [x["table"] for x in data["nftables"] if "table" in x]
        require(len(tables) == 1 and tables[0].get("comment") == args[0], "nft table ownership comment mismatch")
    elif action == "nft-exists":
        data = json.load(sys.stdin)
        sys.exit(0 if any(x.get("table", {}).get("family") == "inet" and x["table"].get("name") == args[0] for x in data["nftables"]) else 1)
    elif action == "routes-free":
        subnet, table = ip.IPv4Network(args[0]), int(args[1])
        for route in json.load(sys.stdin):
            require(table_id(route.get("table", "main")) != table, "routing table already has routes")
            dst = route.get("dst", "default")
            if dst not in ("default", "0.0.0.0/0"):
                require(not ip.IPv4Network(dst, strict=False).overlaps(subnet), f"subnet overlaps existing route {dst}")
    elif action == "rules-free":
        mask, table, priority = number(args[0]), int(args[1]), int(args[2])
        for rule in json.load(sys.stdin):
            require(rule.get("priority") != priority, "policy rule priority is occupied")
            require(table_id(rule.get("table")) != table, "routing table is referenced by an existing rule")
            if "fwmark" in rule:
                require(number(str(rule.get("fwmask", "0xffffffff"))) & mask == 0, "reserved mark mask overlaps an existing rule")
    elif action == "route-owned":
        data = json.load(sys.stdin)
        require(len(data) == 1, "dedicated route table is not exactly one owned route")
        route = data[0]
        require(str(route.get("type")) in ("local", "2") and route.get("dst") in ("default", "0.0.0.0/0") and route.get("dev") == "lo" and str(route.get("protocol")) == args[0] and str(route.get("scope")) in ("host", "254"), "local route changed or is not owned")
        require(set(route) <= {"type", "dst", "dev", "table", "protocol", "scope", "flags"} and not route.get("flags"), "unexpected local route attributes")
    elif action == "rule-owned":
        table, priority, mark, mask, iface, source = args
        all_rules = json.load(sys.stdin)
        data = [r for r in all_rules if r.get("priority") == int(priority) or table_id(r.get("table")) == int(table)]
        require(len(data) == 1, "dedicated rule/table has unexpected references")
        r = data[0]
        require(r.get("priority") == int(priority) and table_id(r.get("table")) == int(table) and r.get("iif") == iface and r.get("src") in (source, source + "/32") and number(str(r.get("fwmark", "0"))) == number(mark) and number(str(r.get("fwmask", "0xffffffff"))) == number(mask), "policy rule changed or is not owned")
        require(set(r) <= {"priority", "src", "dst", "iif", "fwmark", "fwmask", "table", "protocol", "flags"} and r.get("dst", "all") == "all" and not r.get("flags"), "unexpected policy rule selectors")
    elif action == "routes-for":
        print(json.dumps([r for r in json.load(sys.stdin) if table_id(r.get("table", "main")) == int(args[0])]))
    elif action == "rule-exists":
        data = json.load(sys.stdin)
        sys.exit(0 if any(r.get("priority") == int(args[1]) or table_id(r.get("table")) == int(args[0]) for r in data) else 1)
    elif action == "link-exists":
        sys.exit(0 if any(r.get("ifname") == args[0] for r in json.load(sys.stdin)) else 1)
    elif action == "ready":
        data = json.load(sys.stdin)
        require(len(data) == 1 and "UP" in data[0].get("flags", []) and any(a.get("local") == args[0] and a.get("prefixlen") == 30 for a in data[0].get("addr_info", [])), "veth not up at its configured IPv4 /30 address")
    elif action == "empty":
        require(not json.load(sys.stdin), "expected empty resource inventory")
    else:
        raise ValueError("unknown helper action")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, TypeError) as e:
        print(f"network guard: {e}", file=sys.stderr)
        sys.exit(1)
