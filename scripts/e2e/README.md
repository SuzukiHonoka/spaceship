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

Coverage:

- h2c and TLS tunnels: 4 MiB SOCKS5 round trip verified by SHA-256, 40
  concurrent tunnels, HTTP `CONNECT`, absolute-form HTTP GET, SOCKS5 UDP
  associate.
- `basic_auth` accepted and rejected.
- Shutdown under load: 40 established sessions plus a peer frozen with SIGSTOP
  mid-session, then SIGTERM. Both processes must exit within 2s. This is the
  regression that shipped in v2.1.7, where the server called grpc `GracefulStop`
  first and waited on peers that never answered the GOAWAY.
- Restart cycle: stop the server, rebind the port, and confirm a still-running
  client recovers on its own.
- `-stop-timeout` watchdog stays silent on a healthy stop.
