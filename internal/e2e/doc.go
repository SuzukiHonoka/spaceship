// Package e2e holds full-stack tests that drive spaceship's front ends over the
// wire — a real SOCKS5 client against a real SOCKS5 listener — rather than
// calling transports directly.
//
// These complement the tunnel tests in internal/transport/rpc, which cover the
// client↔server gRPC leg. The two legs are deliberately tested separately: the
// router is process-global, so a single test process cannot have the client side
// route a destination to the proxy egress while the server side routes the same
// destination to direct. Chaining both legs in one process would route the
// server's dial straight back into the tunnel.
package e2e
