#!/usr/bin/env python3
"""Unprivileged validation/ownership tests. Never invokes up/down or netlink."""
import json
import os
from pathlib import Path
import subprocess
import sys
import unittest

DEPLOY = Path(__file__).resolve().parent
GUARD = DEPLOY / "network-guard.py"
SCRIPT = DEPLOY / "network.sh"
BASE = dict(
    NETNS="mcr-backends", HOST_IF="mcr-host", PEER_IF="mcr-peer",
    SUBNET="10.203.76.0/30", HOST_IP="10.203.76.1", BACKEND_IP="10.203.76.2",
    NFT_TABLE="mc_router_go", MARK="0x100", MASK="0xff00", ROUTE_TABLE="40076",
    RULE_PRIORITY="1000", ROUTE_PROTOCOL="186", STATE_DIR="/run/mc-router-go-network",
    EGRESS_IF="", TCP_PORTS="443", TCP_DESTINATIONS="0.0.0.0/0", DNS="",
)
TOKEN = "mc-router-go:11111111-1111-1111-1111-111111111111"


def helper(action, *args, data=None):
    return subprocess.run([sys.executable, "-I", str(GUARD), action, *map(str, args)],
                          input=None if data is None else json.dumps(data),
                          text=True, capture_output=True, check=False)


def validate(**kwargs):
    return helper("validate", *(f"{k}={v}" for k, v in (BASE | kwargs).items()))


def plan(*args, overrides=None):
    env = {k: v for k, v in os.environ.items() if not k.startswith("MCR_")}
    env.update(overrides or {})
    return subprocess.run(["bash", str(SCRIPT), "plan", *args],
                          env=env, text=True, capture_output=True, check=False)


class ValidationTests(unittest.TestCase):
    def test_valid_defaults_and_normalization(self):
        p = validate(MARK="256", MASK="65280", TCP_PORTS="443,80,443")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("MARK=0x100\n", p.stdout)
        self.assertIn("TCP_PORTS=80,443\n", p.stdout)

    def test_invalid_configurations(self):
        cases = [
            {"NETNS": "../../x"}, {"NETNS": "bad\nns"}, {"HOST_IF": "lo"},
            {"HOST_IF": "sixteen_char_ifxx"}, {"PEER_IF": "mcr-host"},
            {"NFT_TABLE": "x;flush ruleset"}, {"EGRESS_IF": "eth0\nflush ruleset"},
            {"EGRESS_IF": "mcr-host"}, {"SUBNET": "10.203.76.0/24"},
            {"SUBNET": "10.203.76.1/30"}, {"SUBNET": "203.0.113.0/30"},
            {"HOST_IP": "10.203.76.0"}, {"BACKEND_IP": "10.203.76.1"},
            {"HOST_IP": "::1"}, {"MARK": "0"}, {"MASK": "0"},
            {"MARK": "0x10000", "MASK": "0xff0000"},
            {"MARK": "0x100", "MASK": "0xff"}, {"MASK": "0x100000000"},
            {"MARK": "$(touch /tmp/no)"}, {"ROUTE_TABLE": "52"},
            {"ROUTE_TABLE": "254"}, {"RULE_PRIORITY": "0"},
            {"RULE_PRIORITY": "32766"}, {"ROUTE_PROTOCOL": "2"},
            {"TCP_PORTS": "0"}, {"TCP_PORTS": "65536"}, {"TCP_PORTS": "443;drop"},
            {"TCP_DESTINATIONS": "::/0"}, {"TCP_DESTINATIONS": "10.0.0.1/24"},
            {"DNS": "1.1.1.1"}, {"DNS": "::1", "EGRESS_IF": "eth0"},
            {"STATE_DIR": "relative"}, {"STATE_DIR": "/run/../tmp/x"},
            {"STATE_DIR": "/tmp/a\nNETNS=evil"},
        ]
        for case in cases:
            with self.subTest(case=case):
                self.assertNotEqual(validate(**case).returncode, 0)

    def test_alternate_isolated_resources(self):
        p = validate(NETNS="test-ns", HOST_IF="test-host", PEER_IF="test-peer",
                     SUBNET="192.168.222.0/30", HOST_IP="192.168.222.1",
                     BACKEND_IP="192.168.222.2", NFT_TABLE="test_router", MARK="0x200",
                     MASK="0xff00", ROUTE_TABLE="40123", RULE_PRIORITY="999",
                     ROUTE_PROTOCOL="242", STATE_DIR="/run/test-network")
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_default_plan_limits(self):
        p = plan()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn('iifname "mcr-host" ip saddr 10.203.76.2 meta l4proto tcp socket transparent 1', p.stdout)
        self.assertIn('from 10.203.76.2/32 fwmark 0x100/0xff00', p.stdout)
        self.assertIn('meta mark & 0xffff00ff', p.stdout)
        self.assertIn('oifname "mcr-host" counter drop', p.stdout)
        self.assertIn('input_guard iifname "mcr-host" counter drop', p.stdout)
        self.assertNotIn('masquerade', p.stdout)
        self.assertNotIn('tcp dport', p.stdout)  # client ephemeral port must not be restricted
        self.assertNotIn('flush', p.stdout)

    def test_dotted_interface_sysctl_paths(self):
        p = plan('--host-if', 'proof.host', '--peer-if', 'proof.peer', '--netns', 'proof.backends')
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn('net/ipv4/conf/proof.host/rp_filter=2', p.stdout)
        self.assertIn('net/ipv4/conf/proof.peer/rp_filter=2', p.stdout)
        self.assertIn('net/ipv6/conf/proof.host/disable_ipv6=1', p.stdout)
        self.assertIn('net/ipv6/conf/proof.peer/disable_ipv6=1', p.stdout)
        self.assertIn('iifname "proof.host"', p.stdout)
        self.assertNotIn('net.ipv4.conf.proof.host', p.stdout)

    def test_egress_plan_is_explicit_and_scoped(self):
        p = plan('--egress-if', 'eth0', '--dns', '1.1.1.1,9.9.9.9',
                 '--tcp-ports', '443,80', '--tcp-destinations', '1.0.0.0/8')
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn('ip daddr { 1.1.1.1, 9.9.9.9 } udp dport 53', p.stdout)
        self.assertIn('ip daddr { 1.0.0.0/8 } tcp dport { 80, 443 }', p.stdout)
        self.assertIn('ct state new ct mark set (ct mark & 0xffff00ff)', p.stdout)
        self.assertIn('iifname "mcr-host" oifname "eth0" ip saddr 10.203.76.2', p.stdout)
        self.assertIn('counter masquerade', p.stdout)

    def test_cli_and_environment(self):
        p = plan('--host-if', 'cli-host', overrides={'MCR_HOST_IF': 'env-host', 'MCR_NFT_TABLE': 'test_table'})
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn('cli-host', p.stdout)
        self.assertNotIn('env-host', p.stdout)
        self.assertIn('inet test_table', p.stdout)
        for args in [('--host-if',), ('--unknown', 'x'), ('--force-down',), ('--mark', 'x')]:
            self.assertNotEqual(plan(*args).returncode, 0)


class OwnershipTests(unittest.TestCase):
    def test_mark_and_table_conflicts(self):
        tailscale = [{"priority": 5210, "fwmark": "0x80000", "fwmask": "0xff0000", "table": "254"}]
        self.assertEqual(helper('rules-free', '0xff00', 40076, 1000, data=tailscale).returncode, 0)
        for extra in [
            {"priority": 1000, "table": "254"}, {"priority": 2000, "table": "40076"},
            {"priority": 2000, "fwmark": "0x100", "fwmask": "0xff00"},
            {"priority": 2000, "fwmark": 1},
        ]:
            with self.subTest(extra=extra):
                self.assertNotEqual(helper('rules-free', '0xff00', 40076, 1000, data=tailscale + [extra]).returncode, 0)

    def test_route_conflicts_across_all_tables(self):
        self.assertEqual(helper('routes-free', BASE['SUBNET'], 40076,
                                data=[{"dst": "default", "table": "254"}]).returncode, 0)
        for routes in [[{"dst": "10.203.76.0/24", "table": "52"}],
                       [{"dst": "default", "table": "40076"}],
                       [{"dst": "10.0.0.0/8"}]]:
            self.assertNotEqual(helper('routes-free', BASE['SUBNET'], 40076, data=routes).returncode, 0)

    def test_exact_rule_identity_numeric_json(self):
        rule = {"priority": 1000, "src": "10.203.76.2", "fwmark": "0x100", "fwmask": "0xff00",
                "iif": "mcr-host", "table": "40076"}
        args = (40076, 1000, '0x100', '0xff00', 'mcr-host', '10.203.76.2')
        self.assertEqual(helper('rule-owned', *args, data=[rule]).returncode, 0)
        for change in [{"iif": "eth0"}, {"src": "all"}, {"fwmark": "0x200"},
                       {"table": "40077"}, {"dst": "1.1.1.1"}, {"not": True}]:
            self.assertNotEqual(helper('rule-owned', *args, data=[rule | change]).returncode, 0)
        self.assertNotEqual(helper('rule-owned', *args, data=[rule, rule]).returncode, 0)

    def test_exact_route_identity_numeric_json(self):
        route = {"type": "2", "dst": "default", "dev": "lo", "scope": "254", "protocol": "186"}
        self.assertEqual(helper('route-owned', 186, data=[route]).returncode, 0)
        for change in [{"dev": "eth0"}, {"protocol": "2"}, {"scope": "253"}, {"gateway": "1.1.1.1"}]:
            self.assertNotEqual(helper('route-owned', 186, data=[route | change]).returncode, 0)
        self.assertNotEqual(helper('route-owned', 186, data=[]).returncode, 0)

    def test_resource_tokens(self):
        self.assertEqual(helper('alias', TOKEN, data=[{"ifalias": TOKEN}]).returncode, 0)
        self.assertNotEqual(helper('alias', TOKEN, data=[{}]).returncode, 0)
        data = {"nftables": [{"table": {"family": "inet", "name": "mc_router_go", "comment": TOKEN}}]}
        self.assertEqual(helper('nft-owned', TOKEN, data=data).returncode, 0)
        self.assertNotEqual(helper('nft-owned', 'wrong', data=data).returncode, 0)
        self.assertEqual(helper('nft-exists', 'mc_router_go', data=data).returncode, 0)
        self.assertEqual(helper('nft-exists', 'other', data=data).returncode, 1)

    def test_route_table_filter_handles_numeric_strings(self):
        p = helper('routes-for', 40076, data=[{"table": "40076", "dst": "default"}, {"dst": "default"}])
        self.assertEqual(json.loads(p.stdout), [{"table": "40076", "dst": "default"}])

    def test_address_readiness(self):
        data = [{"flags": ["UP"], "addr_info": [{"local": "10.203.76.1", "prefixlen": 30}]}]
        self.assertEqual(helper('ready', '10.203.76.1', data=data).returncode, 0)
        self.assertNotEqual(helper('ready', '10.203.76.2', data=data).returncode, 0)
        self.assertNotEqual(helper('ready', '10.203.76.1', data=[data[0] | {"flags": []}]).returncode, 0)


if __name__ == '__main__':
    unittest.main()
