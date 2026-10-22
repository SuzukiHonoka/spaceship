package transport

import (
	"context"
	"fmt"
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
	// outboundProtector is optional. Nil means outbound sockets get no extra
	// protection beyond the bypass mark.
	outboundProtector atomic.Pointer[socketProtector]
)

// socketProtector is stored by pointer so SetSocketProtector can swap it
// atomically. The function runs on Spaceship-created outbound sockets.
type socketProtector func(fd int) error

// SetSocketProtector installs a process-wide hook for every outbound socket
// Spaceship creates, including gRPC, direct, and UDP dials, and including
// when the bypass mark is 0. A nil hook disables it.
//
// Android apps that do not exclude their own UID call this before dialing,
// with a function that invokes VpnService.protect(fd). Spaceship does not
// import Android APIs; the embedder injects the callback. A protector error
// fails that dial. The hook is safe for concurrent Set and dial.
func SetSocketProtector(fn func(fd int) error) {
	if fn == nil {
		outboundProtector.Store(nil)
		return
	}
	hook := socketProtector(fn)
	outboundProtector.Store(&hook)
}

func socketProtectorHook() func(int) error {
	hook := outboundProtector.Load()
	if hook == nil {
		return nil
	}
	return *hook
}

// protectOutboundSocket runs the embedder hook. A nil hook is success.
func protectOutboundSocket(fd int) error {
	protect := socketProtectorHook()
	if protect == nil {
		return nil
	}
	if err := protect(fd); err != nil {
		return fmt.Errorf("protect outbound socket: %w", err)
	}
	return nil
}

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
	return VerifyBypassMarkValue(bypassMark.Load())
}

// VerifyBypassMarkValue reports whether mark could be applied to a socket,
// without installing it. Callers use this to decide whether a mark is usable
// before committing to it.
func VerifyBypassMarkValue(mark uint32) error {
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
