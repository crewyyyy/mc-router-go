#!/usr/bin/env python3
"""Bounded fixtures for the isolated namespace test, never production endpoints."""
import argparse
import asyncio
import json
import os
import pathlib
import socket
import statistics
import struct
import threading
import time


def varint(n):
    out = bytearray()
    while True:
        b = n & 127
        n >>= 7
        out.append(b | (128 if n else 0))
        if not n:
            return bytes(out)


def handshake(host):
    raw = host.encode('utf-8')
    payload = b'\x00' + varint(763) + varint(len(raw)) + raw + struct.pack('>H', 25565) + b'\x02'
    return varint(len(payload)) + payload


def exact(conn, n):
    data = bytearray()
    while len(data) < n:
        part = conn.recv(n - len(data))
        if not part:
            raise EOFError('connection closed')
        data.extend(part)
    return bytes(data)


def backend(args):
    if args.address != '10.203.76.2' or args.port != 25567:
        raise ValueError('fixture backend address must be isolated test address')
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind((args.address, args.port))
    srv.listen(256)
    print(json.dumps({'event': 'ready', 'listen': srv.getsockname()}), flush=True)
    pathlib.Path(args.ready).write_text('ready\n')
    lock = threading.Lock()

    def handle(conn, peer):
        try:
            conn.settimeout(15)
            with lock:
                print(json.dumps({'event': 'accepted', 'peer_ip': peer[0], 'peer_port': peer[1]}), flush=True)
            metadata = json.dumps({'peer_ip': peer[0], 'peer_port': peer[1]}).encode()
            conn.sendall(len(metadata).to_bytes(2, 'big') + metadata)
            while data := conn.recv(32768):
                conn.sendall(data)
        except (OSError, EOFError):
            pass
        finally:
            conn.close()

    while True:
        conn, peer = srv.accept()
        threading.Thread(target=handle, args=(conn, peer), daemon=True).start()


async def clients(args):
    if args.address != '198.51.100.1' or args.port != 25565:
        raise ValueError('fixture client address must be isolated test address')
    if not 1 <= args.clients <= 128 or not 1 <= args.rounds <= 100:
        raise ValueError('test workload exceeds fixture limits')
    start = time.monotonic()
    rtts = []
    ready = 0
    gate = asyncio.Event()
    peer_ips = set()
    wire = handshake('HORIZON.EXAMPLE.\x00FML3\x00') + b'optimistic-login-payload\x00\xff'

    async def client(index):
        nonlocal ready
        reader, writer = await asyncio.open_connection(args.address, args.port, local_addr=('198.51.100.2', 0))
        try:
            writer.write(wire)
            await writer.drain()
            size = int.from_bytes(await reader.readexactly(2), 'big')
            if not 0 < size <= 1024:
                raise AssertionError('unexpected fixture header')
            peer = json.loads(await reader.readexactly(size))
            peer_ips.add(peer['peer_ip'])
            if peer['peer_ip'] != args.expected_peer:
                raise AssertionError(f"backend saw {peer['peer_ip']}, expected {args.expected_peer}")
            echoed = await reader.readexactly(len(wire))
            if echoed != wire:
                raise AssertionError('handshake/metadata/pipelined bytes changed')
            ready += 1
            if ready == args.clients:
                gate.set()
            await gate.wait()
            for n in range(args.rounds):
                payload = index.to_bytes(2, 'big') + n.to_bytes(2, 'big') + b'X' * 1020
                tick = time.monotonic()
                writer.write(payload)
                await writer.drain()
                if await reader.readexactly(len(payload)) != payload:
                    raise AssertionError('echo payload changed')
                rtts.append((time.monotonic() - tick) * 1000)
                await asyncio.sleep(0.01)
        finally:
            writer.close()
            await writer.wait_closed()

    async with asyncio.timeout(20):
        await asyncio.gather(*(client(i) for i in range(args.clients)))
    ordered = sorted(rtts)
    print(json.dumps({'event': 'pass', 'clients': args.clients, 'round_trips': len(rtts),
                      'client_ip': '198.51.100.2', 'backend_peer_ips': sorted(peer_ips),
                      'median_rtt_ms': round(statistics.median(rtts), 3),
                      'p95_rtt_ms': round(ordered[int((len(ordered) - 1) * 0.95)], 3),
                      'elapsed_s': round(time.monotonic() - start, 3),
                      'note': 'synthetic namespace TCP fixture, not Minecraft TPS/capacity'}), flush=True)


def wait_ready(args):
    until = time.monotonic() + 8
    while time.monotonic() < until:
        try:
            os.kill(args.pid, 0)
        except ProcessLookupError:
            raise RuntimeError('test process exited before ready')
        if args.ready and pathlib.Path(args.ready).exists():
            return
        if args.listen:
            with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
                probe.settimeout(0.1)
                if probe.connect_ex((args.listen, args.port)) == 0:
                    return
        time.sleep(0.05)
    raise TimeoutError('test process did not become ready')


p = argparse.ArgumentParser()
p.add_argument('mode', choices=('backend', 'client', 'wait'))
p.add_argument('--address', default='198.51.100.1')
p.add_argument('--port', type=int, default=25565)
p.add_argument('--expected-peer', default='198.51.100.2')
p.add_argument('--clients', type=int, default=1)
p.add_argument('--rounds', type=int, default=10)
p.add_argument('--ready')
p.add_argument('--listen')
p.add_argument('--pid', type=int)
args = p.parse_args()
if args.mode == 'backend':
    backend(args)
elif args.mode == 'client':
    asyncio.run(clients(args))
else:
    wait_ready(args)
