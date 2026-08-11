# Spaceship

Spaceship is a tool designed to create secure tunnels to remote networks.

# Technologies Used

- gRPC
- Protocol Buffers (protobuf)

## Usage

```shell
# spaceship -h
Usage of spaceship:
  -c string
        config path (default "./config.json")
  -interval duration
        show stats interval in seconds (default 1s)
  -s    show stats
  -v    show spaceship version
```

## Linux TCP Transparent Redirect

On Linux, a client can accept TCP connections sent to it by the `REDIRECT`
target in the `iptables` or `ip6tables` `nat` table. Enable the listener with
`listen_redirect`:

```json
{
  "role": "client",
  "server_addr": "tunnel.example.com:443",
  "tls": true,
  "uuid": "00000000-0000-0000-0000-000000000001",
  "listen_redirect": "0.0.0.0:12345",
  "redirect": {
    "max_connections": 1024,
    "bypass_mark": 21328
  }
}
```

The listener reads `SO_ORIGINAL_DST` from each accepted socket, routes the
recovered destination IP through the configured Spaceship routes, and carries
the TCP stream through the selected egress. It is Linux-only and TCP-only.
Because REDIRECT supplies an IP address rather than a hostname, `cidr`, `exact`
IP, and `default` routes are useful here; domain routes cannot match unless a
separate future traffic-sniffing feature recovers a hostname.

`redirect.max_connections` bounds accepted sessions and their proxy goroutines.
Omit it or set it to `0` to use the default of 1024; the accepted maximum is
65536, because each admitted session owns a socket, a goroutine, and an egress
stream. When the limit is reached, new connections remain in the kernel listen
backlog until capacity is available; size the limit together with the service
file-descriptor limit and expected tunnel capacity. The `redirect` section
configures that listener, so it is rejected without `listen_redirect` rather
than silently ignored.

For traffic forwarded from a LAN, put rules in a dedicated chain and scope the
jump to the intended ingress interface. Adapt the interface, exclusions, and
port to the host:

```shell
iptables -t nat -N SPACESHIP_REDIRECT
iptables -t nat -A SPACESHIP_REDIRECT -d 127.0.0.0/8 -j RETURN
iptables -t nat -A SPACESHIP_REDIRECT -m addrtype --dst-type LOCAL -j RETURN
iptables -t nat -A SPACESHIP_REDIRECT -p tcp -j REDIRECT --to-ports 12345
iptables -t nat -A PREROUTING -i lan0 -p tcp -j SPACESHIP_REDIRECT
```

For traffic originating on the Spaceship host, exempting Spaceship's own egress
is mandatory. The listener cannot distinguish a newly redirected application
flow from Spaceship's own outbound gRPC or direct egress by destination alone.
Without an exemption the listener captures its own egress, routes it, captures
the result, and repeats: a single connection exhausts `max_connections` in
well under a second and the frontend stops accepting traffic.

Set `redirect.bypass_mark` and exempt that mark. Spaceship applies it with
`SO_MARK` to its gRPC control connection, direct TCP/UDP egress, forward-proxy
connection, and pure-Go resolver sockets. Prefer it over `-m owner`, which
silently fails to match when Spaceship runs as root. Keep the owner rule too,
as defense in depth, along with the tunnel server address and local
control-plane networks:

```shell
iptables -t nat -N SPACESHIP_LOCAL
iptables -t nat -A SPACESHIP_LOCAL -m mark --mark 0x5350/0xffffffff -j RETURN
iptables -t nat -A SPACESHIP_LOCAL -m owner --uid-owner spaceship -j RETURN
iptables -t nat -A SPACESHIP_LOCAL -d 127.0.0.0/8 -j RETURN
iptables -t nat -A SPACESHIP_LOCAL -m addrtype --dst-type LOCAL -j RETURN
iptables -t nat -A SPACESHIP_LOCAL -d 192.0.2.10/32 -j RETURN
iptables -t nat -A SPACESHIP_LOCAL -p tcp -j REDIRECT --to-ports 12345
iptables -t nat -A OUTPUT -p tcp -j SPACESHIP_LOCAL
```

`bypass_mark` is opt-in and defaults to no marking, because `SO_MARK` needs
network-administration capability (normally `CAP_NET_ADMIN`) that a LAN-only
`PREROUTING` deployment does not otherwise require. Spaceship logs a warning
at startup when `listen_redirect` is set without it, and fails at startup with
one clear error if the mark is configured but the process lacks the capability.
`21328` (`0x5350`) is the suggested value. When TUN is also enabled its
`bypass_mark` is inherited automatically; setting a different value here is
rejected, because a process has exactly one outbound socket mark.

Replace `192.0.2.10` with every IP used by `server_addr`; do not use the
documentation address literally. Add explicit exclusions for management and
other networks that must stay local.

For IPv6 interception, set `"ipv6": true`, listen on `[::]:12345`, and install
equivalent `ip6tables` rules. Without `"ipv6": true`, Spaceship intentionally
installs an IPv6 block route. Exclude `::1/128`, multicast, and link-local
destinations unless they deliberately use a direct route; a link-local scope
identifier is meaningful only in the local network namespace.

`SO_ORIGINAL_DST` depends on a conntrack entry. The REDIRECT rule and Spaceship
listener must run in the same network namespace, with IPv4/IPv6 conntrack and
NAT support available. In production, persist rules with the host's firewall
manager or `iptables-restore`, restrict the listener to trusted ingress, and
remove the jump before deleting or changing its chain. Verify the dedicated UID
exemption and tunnel-server exclusions before enabling the `OUTPUT` jump.

On a Linux build host with `unshare`, `ip`, and `iptables`, the opt-in test below
creates a disposable network namespace and validates the real conntrack,
`SO_ORIGINAL_DST`, routing, and TCP stream path. Add the second environment
variable to cover IPv6 with `ip6tables`:

```shell
SPACESHIP_REDIRECT_INTEGRATION=1 \
SPACESHIP_REDIRECT_INTEGRATION_IPV6=1 \
go test -count=1 -run '^TestNetfilterRedirectIntegration$' ./internal/redirect
```

## Linux TUN Frontend

The client can terminate IP packets from a Linux TUN interface in its own
gVisor network stack. Spaceship implements this frontend directly; it does not
embed or invoke `tun2socks`. General TUN traffic is TCP-only. When DNS hijacking
is enabled, UDP is admitted only for destination port 53 so classic DNS works
without enabling a general UDP tunnel.

```json
{
  "role": "client",
  "server_addr": "tunnel.example.com:443",
  "tls": true,
  "uuid": "00000000-0000-0000-0000-000000000001",
  "ipv6": true,
  "mux": 2,
  "block_ipv6_dns": false,
  "tun": {
    "name": "spaceship0",
    "mtu": 1500,
    "route_mode": "manual",
    "bypass_mark": 21328,
    "max_connections": 4096,
    "max_pending_connections": 1024,
    "dns_hijack": {
      "enabled": true,
      "query_timeout_seconds": 5,
      "tcp_idle_timeout_seconds": 10,
      "max_in_flight": 256,
      "max_in_flight_per_connection": 192
    }
  }
}
```

Zero-valued limits select the values shown above. `dns_hijack.max_in_flight`
has a hard maximum of 1024 because every active UDP DNS flow owns a receive
buffer of up to 64 KiB for its idle lifetime; the ceiling bounds those buffers
to about 64 MiB. `bypass_mark` also defaults to decimal `21328` (`0x5350`).
With TUN enabled, an omitted or zero `mux` selects the minimum persistent gRPC
pool needed for the configured TCP and DNS concurrency; the defaults above
require two connections. An explicit smaller value is rejected during config
validation. A non-zero `mux` is a warm minimum: the shared pool grows when all
current connections reach Spaceship's native per-connection stream limit, up
to 255 persistent connections. This lets TUN, SOCKS, HTTP, REDIRECT, and DNS
share capacity without silently overloading the initial TUN-sized pool.
Outside TUN mode, `mux: 0` retains the legacy unpooled behavior.

The growth threshold matches the native Spaceship server's HTTP/2 limit. If an
intermediary advertises a lower concurrent-stream limit, configure enough
initial `mux` connections to cover the expected peak at that lower value;
otherwise the intermediary can queue streams before the local pool reaches its
growth threshold. Server-wide Proxy admission still applies across every
connection, so adding transports cannot bypass the server resource boundary.
Spaceship applies that mark with `SO_MARK` to its gRPC control connection,
direct TCP/UDP egress, forward-proxy connection, and pure-Go resolver sockets.
The policy-routing rules must exempt that mark from the TUN or the tunnel will
recursively capture its own control traffic.

`route_mode` currently accepts only `manual`. Spaceship creates the named
`IFF_TUN|IFF_NO_PI` interface, sets its MTU, and brings it up, but deliberately
does not change host addresses, routes, or policy rules. This keeps a bad config
from replacing a production host's default route. One local-host pattern is:

```shell
# Run after Spaceship has created spaceship0.
ip addr add 198.18.0.1/30 dev spaceship0
ip route add default dev spaceship0 table 100

# Spaceship egress must use the ordinary routing table.
ip rule add pref 100 fwmark 0x5350/0xffffffff lookup main

# Example: capture other unmarked, non-local IPv4 traffic.
# The kernel's priority-0 local-table rule remains ahead of this rule.
ip rule add pref 110 not fwmark 0x5350/0xffffffff lookup 100
```

Treat that as a starting point, not a copy-paste policy for every host. Prefer
scoping the capture rule to an application UID, cgroup-applied mark, source
subnet, or dedicated network namespace. Add explicit higher-priority rules for
the Spaceship server IPs and management networks as defense in depth. If IPv6
is required, assign an appropriate IPv6 address and install equivalent `ip -6
route` and `ip -6 rule` entries. Remove the capture rule before stopping
Spaceship; otherwise new connections will be black-holed by a route whose TUN
reader no longer exists.

Creating the interface and setting `SO_MARK` require Linux network
administration capability (normally `CAP_NET_ADMIN`) and access to
`/dev/net/tun`. The process also needs enough file descriptors for the
configured connection limit. An externally provisioned descriptor may be
passed as `tun.file_descriptor`; Spaceship duplicates it, validates that it is
a single-queue `IFF_TUN|IFF_NO_PI` device without a virtio-net header, uses the
interface's actual MTU, and closes only its duplicate. The descriptor's shared
file status is made nonblocking as required by the gVisor endpoint.

With `dns_hijack.enabled`, every TCP or UDP flow whose original destination
port is 53 is intercepted before normal route selection. Spaceship preserves
the DNS wire message, including flags, response codes, EDNS, DNSSEC records,
and authority/additional sections, and sends it over the authenticated gRPC
connection. The server queries only its configured `dns` resolver (or the
server's existing default of `8.8.8.8:53`). An RPC or upstream failure becomes
DNS `SERVFAIL`; the client never falls back to the original destination or a
local resolver. DNS-over-TCP length framing, multiple queries per connection,
and bounded pipelining are supported.

Pipelining is bounded twice. `dns_hijack.max_in_flight` caps concurrent DNS
RPCs across all clients, and `max_in_flight_per_connection` caps those held by
any single DNS-over-TCP connection. Without the second bound, one client that
pipelines aggressively holds every slot and every other client behind the TUN
receives `SERVFAIL` until its queries drain.

Zero sets the per-connection ceiling to roughly three quarters of
`max_in_flight` — 192 of the default 256, far more headroom than a stub
resolver pipelines — and leaves pools of four or fewer unrestricted, where
capping would cost more pipelining than it buys. It may not exceed
`max_in_flight`. This is a per-connection ceiling rather than a reservation:
it stops any one connection monopolising the pool, but several busy
connections can still fill it between them, which is what the global bound is
for.

Reaching the per-connection ceiling applies backpressure instead of failing.
Spaceship stops reading that socket until one of the connection's own queries
completes, letting TCP flow control slow the client, so a deep pipeline is
delayed rather than answered `SERVFAIL` while the shared pool still has room.
Exhausting the global bound still returns `SERVFAIL`, because waiting there
would stall one client on another's work. UDP needs no equivalent: each UDP
flow answers one query at a time.

The server applies non-blocking global and per-user concurrency and token-bucket
rate limits before starting an upstream exchange. The following shows the
built-in defaults explicitly:

```json
{
  "role": "server",
  "users": [{"uuid": "00000000-0000-0000-0000-000000000001"}],
  "dns_exchange": {
    "max_concurrent": 1024,
    "max_concurrent_per_user": 256,
    "queries_per_second": 4096,
    "queries_per_second_per_user": 1024,
    "burst": 1024,
    "burst_per_user": 256
  },
  "proxy_sessions": {
    "max_concurrent": 8192,
    "max_concurrent_per_user": 4096,
    "new_sessions_per_second": 8192,
    "new_sessions_per_second_per_user": 4096,
    "burst": 8192,
    "burst_per_user": 4096,
    "handshake_timeout_seconds": 10
  }
}
```

Zero selects the shown default for each field. When a rate is explicitly
lowered and its burst remains zero, the implicit burst is capped at that rate;
an explicitly configured larger burst is preserved. A per-user value cannot
exceed its global counterpart. DNS admission covers both the wire exchange and
the legacy record-oriented RPC; legacy batches are limited to 16 items.
Saturated wire RPCs return gRPC `ResourceExhausted`, which the local DNS and TUN
frontends convert to DNS `SERVFAIL` without fallback. Proxy admission is
enforced across all HTTP/2 connections, and an authenticated stream that does
not send its first routing header within the configured timeout is closed. This
avoids hidden queues and idle-stream resource retention.

Monotonic request, forwarding, upstream-failure, timeout, and rejection
counters are exposed in the `dns_exchange` and `proxy_sessions` objects
returned by the loopback management `/api/stats` endpoint.

For a rolling upgrade, deploy servers with the wire DNS RPC before enabling
`dns_hijack` on clients. A new client connected to an older server receives
gRPC `Unimplemented`, returns DNS `SERVFAIL`, and deliberately does not bypass
the tunnel through a local or destination resolver.

All non-DNS UDP traffic remains unsupported and is rejected by the netstack with
an ICMP unreachable, so a QUIC client falls back to TCP immediately instead of
stalling. Note that this applies to DNS too when `dns_hijack` is disabled: the
netstack then registers no UDP protocol at all, and DNS sent through the TUN
fails. Either enable `dns_hijack` or exempt port 53 from the capture rule so
those queries never enter the TUN. Spaceship logs a warning at startup when TUN
is enabled without DNS hijacking.
Because TUN destinations are IP literals, Spaceship route matching has the same
constraint as transparent REDIRECT: CIDR, exact-IP, and default rules apply,
while domain rules cannot infer a hostname.

The opt-in Linux integration test creates an isolated network namespace and
checks the real TUN device, kernel route, gVisor TCP handshake/stream, and
TCP/UDP DNS interception:

```shell
SPACESHIP_TUN_INTEGRATION=1 \
go test -count=1 -run '^TestKernelTUNIntegration$' ./internal/tun
```

## Nginx Reserve Proxy Configuration

```nginx
...
    location /proxy. {
        grpc_intercept_errors on;
        grpc_socket_keepalive on;
        grpc_send_timeout 3600s;
        grpc_read_timeout 3600s;
        grpc_pass grpc://127.0.0.1:12345;
    }
...
```

Note that `proxy` is the current proto source package name

## Safety

Spaceship currently uses pure gRPC with the insecure option. For secure communication, it is highly recommended to set
up a reverse proxy with TLS, such as `Nginx + TLS`.

## Development Status

The program is still under development. Contributions via pull requests are greatly appreciated.

## Legal Disclaimer

This program is provided "as is," with no warranties or guarantees. It is available only to repository members, and
sharing it with others is strictly prohibited. Users must adhere to the laws of their respective countries. **Any
illegal use of this program is strictly prohibited.**
