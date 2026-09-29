# End-to-end harness

Drives spaceship as real processes — a server binary and a client binary talking
over a real gRPC tunnel — and exercises it through the front ends an operator
actually uses.

The in-tree `internal/e2e` package deliberately covers one leg at a time: the
router is process-global, so a single test process cannot route a destination to
the proxy egress on the client side and to direct on the server side. Separate
processes are the only way to cover both legs at once, which is what this
harness does.

It is Unix-only, and built only on those platforms. Shutdown coverage is the
point of the harness, and it drives that with POSIX signals — `SIGTERM` to stop
a process, `SIGQUIT` for a stack dump, and `SIGSTOP` to freeze a peer so it
stays connected while no longer reading. Windows supports none of that
meaningfully, so the package carries a `unix` build constraint rather than
compiling into something that cannot work.

```bash
go build -o /tmp/spaceship ./cmd/spaceship && go run ./scripts/e2e /tmp/spaceship /tmp/spaceship-e2e
```

Arguments are the binary under test and a scratch directory for configs, keys,
and per-process logs. Exit status is non-zero if any check fails.

Privileged Linux checks (transparent redirect, TUN device lifecycle) need root,
`unshare`, `ip`, `iptables`, and `/dev/net/tun`. Without them the harness
records `SKIP` rather than failing, so a Darwin or unprivileged run still
covers the portable suites. A full Linux pass under Docker looks like:

```bash
docker build -f scripts/e2e/Dockerfile.linux -t spaceship-linux-test:local scripts/e2e
docker run --rm --privileged --device /dev/net/tun \
  -v "$PWD:/src:ro" spaceship-linux-test:local \
  sh -ec 'go test -race -count=1 ./...
    SPACESHIP_REDIRECT_INTEGRATION=1 SPACESHIP_REDIRECT_INTEGRATION_IPV6=1 \
      SPACESHIP_TUN_INTEGRATION=1 go test -race -count=1 -v \
      ./internal/redirect ./internal/tun \
      -run "TestNetfilterRedirectIntegration|TestKernelTUNIntegration"
    go build -o /tmp/spaceship ./cmd/spaceship
    mkdir -p /tmp/spaceship-e2e
    go run ./scripts/e2e /tmp/spaceship /tmp/spaceship-e2e'
```

Run only trusted source with these privileges. The repository is mounted
read-only, and network rules are installed in disposable network namespaces;
do not use host networking or mount the Docker socket into this container.

Performance regression checks cover ordinary/parallel pool checkouts, saturated
pool growth, and legacy DNS capability caching:

```bash
go test ./internal/transport/rpc/client ./internal/dns -run '^$' \
  -bench 'BenchmarkConnQueue|BenchmarkServeDNSLegacy' -benchmem -count=5
```

`new-conns/burst` should be 1 and cached `RPCs/query` should be 1 (versus 2
without the cache). Compare timing on an otherwise idle host; the DNS mock
measures allocations and RPC counts, not network latency.

Compare steady-state direct TCP forwarding with the real gRPC tunnel:

```bash
GOMAXPROCS=4 go test -p=1 ./internal/transport/direct ./internal/transport/rpc \
  -run '^$' -bench 'BenchmarkDirect_Proxy|BenchmarkEndToEnd_TCPTunnel$|BenchmarkEndToEnd_TCPTunnel_Latency' \
  -benchmem -benchtime=2s -count=5
```

Both paths warm up four round trips before timing and exclude teardown.
An operation is one echoed chunk; MB/s counts request plus response bytes,
not one-way link speed. Latency benchmarks report round-trip p50/p99 separately.
Use the same Go version, CPU settings, and benchmark harness for before/after
comparisons, alternate run order, and report medians and spread. These are h2c
loopback measurements, not TLS/WAN capacity guarantees. Codec microbenchmarks
measure encoding/decoding costs, not network throughput.
The direct reference is `Direct.Proxy`, not a raw TCP socket. Both drivers feed
an `io.Pipe` and consume an in-memory sink; Go's `ReaderFrom`/`WriterTo` paths
can use different copy sizes than the configured transport buffer. These
numbers do not describe socket-to-socket splice throughput or a real WAN.

Measure continuous echoed traffic separately (no per-chunk round-trip barrier):

```bash
GOMAXPROCS=4 go test -p=1 ./internal/transport/direct ./internal/transport/rpc \
  -run '^$' -bench 'TCPStream$' -benchmem -benchtime=3s -count=5
```

Both use the same workload driver, warm up 4 MiB, and wait for all echoed bytes
before stopping the timer. One operation is a 1 MiB write and MB/s still counts
both directions. The sink counts bytes without a per-operation timer; it does
not check content integrity (the integration tests do that). Continuous-stream
results must not be substituted for the stop-and-wait throughput or RTT results.
`ops/s` is completed workload operations per second, **not** syscall count;
`ns/op` is time per workload operation, and `allocs/op` counts heap allocations.

Use `-bench 'TCPStream(TLS)?$'` to include the continuous-stream TLS variant.
For encrypted stop-and-wait throughput and latency, use
`-bench 'BenchmarkEndToEnd_TCPTunnelTLS($|_Latency)'`. These variants use the
same workload and a verified local test certificate; TLS setup is outside the
timed region. Compare TLS against TLS when evaluating a change, and identify
the encryption difference when comparing it against unencrypted direct TCP.

Coverage:

- h2c and TLS tunnels: 4 MiB SOCKS5 round trip verified by SHA-256, 40
  concurrent tunnels, HTTP `CONNECT`, absolute-form HTTP GET, SOCKS5 UDP
  associate.
- `basic_auth` accepted and rejected.
- DNS front end: client `listen_dns` through the authenticated DnsExchange RPC
  to the server's configured upstream resolver.
- Server `proxy_sessions.max_concurrent` refuses work beyond the ceiling and
  recovers capacity when sessions end.
- Route `block` egress refuses a matching destination.
- Linux transparent REDIRECT: netfilter capture in a disposable netns, original
  destination recovery, and bypass-mark loop protection.
- Linux TUN: config creates a real interface with the configured MTU; SIGTERM
  removes it.
- Shutdown under load: 40 established sessions plus a peer frozen with SIGSTOP
  mid-session, then SIGTERM. Both processes must exit within 2s. This is the
  regression that shipped in v2.1.7, where the server called grpc `GracefulStop`
  first and waited on peers that never answered the GOAWAY.
- Restart cycle: stop the server, rebind the port, and confirm a still-running
  client recovers on its own.
- `-stop-timeout` watchdog stays silent on a healthy stop.
