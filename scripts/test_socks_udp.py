#!/usr/bin/env python3
"""Test SOCKS5 UDP ASSOCIATE via a spaceship (or any RFC 1928) client.

Default target is the local spaceship SOCKS listener and Cloudflare DNS so a
single datagram round-trip proves the associate + tunnel path.

Examples:
  python3 scripts/test_socks_udp.py
  python3 scripts/test_socks_udp.py --socks 127.0.0.1:10818
  python3 scripts/test_socks_udp.py --user u --password p
"""

from __future__ import annotations

import argparse
import socket
import struct
import sys


def dns_query_example_com() -> bytes:
    """Minimal DNS query for example.com A (RD set)."""
    tid = b"\xab\xcd"
    flags = b"\x01\x00"
    counts = b"\x00\x01\x00\x00\x00\x00\x00\x00"
    qname = b""
    for part in b"example.com".split(b"."):
        qname += bytes([len(part)]) + part
    qname += b"\x00\x00\x01\x00\x01"
    return tid + flags + counts + qname


def parse_hostport(s: str) -> tuple[str, int]:
    host, _, port = s.rpartition(":")
    if not host or not port:
        raise argparse.ArgumentTypeError(f"expected host:port, got {s!r}")
    return host, int(port)


def socks5_connect(socks: tuple[str, int], user: str | None, password: str | None) -> socket.socket:
    ctrl = socket.create_connection(socks, 5)
    if user is not None:
        # Method advertisement: user/pass only.
        ctrl.sendall(b"\x05\x01\x02")
        resp = ctrl.recv(2)
        if resp != b"\x05\x02":
            ctrl.close()
            raise SystemExit(f"server did not accept user/pass auth: {resp!r}")
        u = user.encode()
        p = (password or "").encode()
        if len(u) > 255 or len(p) > 255:
            ctrl.close()
            raise SystemExit("username/password too long for SOCKS5")
        ctrl.sendall(b"\x01" + bytes([len(u)]) + u + bytes([len(p)]) + p)
        auth_resp = ctrl.recv(2)
        if auth_resp != b"\x01\x00":
            ctrl.close()
            raise SystemExit(f"user/pass auth failed: {auth_resp!r}")
    else:
        ctrl.sendall(b"\x05\x01\x00")
        resp = ctrl.recv(2)
        if resp != b"\x05\x00":
            ctrl.close()
            raise SystemExit(
                f"auth failed: {resp!r} (need --user/--password if basic_auth is set)"
            )
    return ctrl


def socks5_udp_associate(ctrl: socket.socket) -> tuple[str, int]:
    # ASSOCIATE with 0.0.0.0:0
    ctrl.sendall(b"\x05\x03\x00\x01\x00\x00\x00\x00\x00\x00")
    reply = ctrl.recv(10)
    if len(reply) < 10 or reply[0] != 5:
        raise SystemExit(f"bad associate reply: {reply!r}")
    if reply[1] != 0:
        # 07 = command not supported (UDP disabled / no UDP-capable route)
        raise SystemExit(
            f"ASSOCIATE rejected, REP={reply[1]} (0=ok, 7=UDP not supported)"
        )
    if reply[3] != 1:
        raise SystemExit(f"unexpected BND.ADDR type {reply[3]}, want IPv4")
    bind_ip = socket.inet_ntoa(reply[4:8])
    bind_port = struct.unpack("!H", reply[8:10])[0]
    if bind_ip in ("0.0.0.0", ""):
        bind_ip = "127.0.0.1"
    return bind_ip, bind_port


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument(
        "--socks",
        default="127.0.0.1:10818",
        type=parse_hostport,
        help="SOCKS5 address (default 127.0.0.1:10818)",
    )
    ap.add_argument(
        "--target",
        default="1.1.1.1:53",
        type=parse_hostport,
        help="UDP destination host:port (default 1.1.1.1:53 DNS)",
    )
    ap.add_argument("--user", default=None, help="SOCKS username if basic_auth is enabled")
    ap.add_argument("--password", default=None, help="SOCKS password")
    ap.add_argument("--timeout", type=float, default=5.0, help="UDP recv timeout seconds")
    args = ap.parse_args()

    ctrl = socks5_connect(args.socks, args.user, args.password)
    try:
        bind_ip, bind_port = socks5_udp_associate(ctrl)
        print(f"UDP relay bound at {bind_ip}:{bind_port}")

        host, port = args.target
        header = b"\x00\x00\x00\x01" + socket.inet_aton(host) + struct.pack("!H", port)
        payload = dns_query_example_com()

        udp = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        udp.settimeout(args.timeout)
        try:
            udp.sendto(header + payload, (bind_ip, bind_port))
            data, _ = udp.recvfrom(65535)
        finally:
            udp.close()

        print(f"got {len(data)} bytes back (SOCKS header + DNS answer)")
        if len(data) < 12:
            raise SystemExit("response too short")
        print("PASS: SOCKS5 UDP ASSOCIATE works through the tunnel")
    finally:
        ctrl.close()


if __name__ == "__main__":
    main()
