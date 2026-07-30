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
    "max_connections": 1024
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
Omit it or set it to `0` to use the default of 1024. When the limit is reached,
new connections remain in the kernel listen backlog until capacity is
available; size the limit together with the service file-descriptor limit and
expected tunnel capacity.

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

For traffic originating on the Spaceship host, a dedicated OS account and the
owner exemption below are mandatory. The listener cannot distinguish a newly
redirected application flow from Spaceship's own outbound gRPC or direct
egress by destination alone. Also exempt the tunnel server address and local
control-plane networks as defense in depth:

```shell
iptables -t nat -N SPACESHIP_LOCAL
iptables -t nat -A SPACESHIP_LOCAL -m owner --uid-owner spaceship -j RETURN
iptables -t nat -A SPACESHIP_LOCAL -d 127.0.0.0/8 -j RETURN
iptables -t nat -A SPACESHIP_LOCAL -m addrtype --dst-type LOCAL -j RETURN
iptables -t nat -A SPACESHIP_LOCAL -d 192.0.2.10/32 -j RETURN
iptables -t nat -A SPACESHIP_LOCAL -p tcp -j REDIRECT --to-ports 12345
iptables -t nat -A OUTPUT -p tcp -j SPACESHIP_LOCAL
```

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
