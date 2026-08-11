package transport

import (
	"context"
	"net"
	"sync/atomic"
	"time"
)

// DefaultBypassMark is the policy-routing mark Spaceship applies to its own
// outbound sockets when a frontend needs that egress exempted from a catch-all
// capture rule. The TUN and transparent REDIRECT frontends share it because a
// process has exactly one outbound mark.
const DefaultBypassMark uint32 = 0x5350

var (
	bypassMark       atomic.Uint32
	outboundResolver atomic.Pointer[net.Resolver]
)

func init() {
	outboundResolver.Store(net.DefaultResolver)
}

// SetBypassMark configures the Linux policy-routing mark applied to outbound
// sockets created by Spaceship. A zero mark disables socket marking.
func SetBypassMark(mark uint32) {
	bypassMark.Store(mark)
}

// BypassMark returns the currently configured outbound socket mark.
func BypassMark() uint32 {
	return bypassMark.Load()
}

// VerifyBypassMark reports whether the currently configured mark can actually
// be applied to a socket. Setting SO_MARK needs network-administration
// capability, so configuration calls this to fail startup with one actionable
// error instead of letting every later dial fail with EPERM.
func VerifyBypassMark() error {
	mark := bypassMark.Load()
	if mark == 0 {
		return nil
	}
	return verifyBypassMark(mark)
}

// NewOutboundDialer returns a dialer used for application egress. Both the
// destination socket and any pure-Go DNS sockets opened while resolving its
// hostname use the configured bypass mark.
func NewOutboundDialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{
		Timeout:  timeout,
		Resolver: OutboundResolver(),
		Control:  outboundSocketControl,
	}
}

// SetOutboundResolver atomically replaces the immutable resolver policy used by
// Spaceship-created dials. Existing dials retain their snapshot.
func SetOutboundResolver(resolver *net.Resolver) {
	if resolver == nil {
		resolver = NewSystemResolver(0)
	}
	outboundResolver.Store(resolver)
}

// OutboundResolver returns the current resolver policy.
func OutboundResolver() *net.Resolver {
	return outboundResolver.Load()
}

// NewSystemResolver returns a pure-Go resolver whose connections to the name
// servers from resolv.conf use the configured bypass mark. PreferGo is required:
// libc resolver sockets cannot be marked by this process.
func NewSystemResolver(timeout time.Duration) *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialer := &net.Dialer{
				Timeout: timeout,
				Control: outboundSocketControl,
			}
			return dialer.DialContext(ctx, network, address)
		},
	}
}

// NewFixedResolver returns a pure-Go resolver that always uses address while
// applying the configured bypass mark to its TCP and UDP sockets.
func NewFixedResolver(address string, timeout time.Duration) *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dialer := &net.Dialer{
				Timeout: timeout,
				Control: outboundSocketControl,
			}
			return dialer.DialContext(ctx, network, address)
		},
	}
}
