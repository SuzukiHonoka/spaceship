package transport

import (
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOutboundResolverSwapIsConcurrentSafe(t *testing.T) {
	original := OutboundResolver()
	t.Cleanup(func() { SetOutboundResolver(original) })

	const iterations = 1000
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for range iterations {
			SetOutboundResolver(NewSystemResolver(time.Second))
		}
	}()
	go func() {
		defer workers.Done()
		for range iterations {
			resolver := OutboundResolver()
			if resolver == nil {
				t.Error("OutboundResolver() returned nil")
				return
			}
			dialer := NewOutboundDialer(time.Second)
			if dialer.Resolver == nil || dialer.Control == nil {
				t.Errorf("NewOutboundDialer() = %+v", dialer)
				return
			}
		}
	}()
	workers.Wait()
}

func TestBypassMarkLifecycle(t *testing.T) {
	original := BypassMark()
	t.Cleanup(func() { SetBypassMark(original) })

	SetBypassMark(0x5350)
	if got := BypassMark(); got != 0x5350 {
		t.Fatalf("BypassMark() = %#x, want 0x5350", got)
	}
	SetBypassMark(0)
	if got := BypassMark(); got != 0 {
		t.Fatalf("BypassMark() after reset = %#x", got)
	}
}

func TestVerifyBypassMarkAllowsDisabledMarking(t *testing.T) {
	original := BypassMark()
	t.Cleanup(func() { SetBypassMark(original) })

	SetBypassMark(0)
	if err := VerifyBypassMark(); err != nil {
		t.Fatalf("VerifyBypassMark() with marking disabled = %v, want nil", err)
	}
}

// VerifyBypassMark reports a real capability, so its result differs by platform
// and privilege. Both outcomes are valid; what must hold is that a successful
// verification means outbound dials can also apply the mark.
func TestVerifyBypassMarkAgreesWithOutboundDialer(t *testing.T) {
	original := BypassMark()
	t.Cleanup(func() { SetBypassMark(original) })

	SetBypassMark(DefaultBypassMark)
	verifyErr := VerifyBypassMark()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback listener unavailable: %v", err)
	}
	defer func() { _ = listener.Close() }()

	conn, dialErr := NewOutboundDialer(3*time.Second).Dial("tcp4", listener.Addr().String())
	if dialErr == nil {
		_ = conn.Close()
	}
	if verifyErr == nil && dialErr != nil {
		t.Fatalf("VerifyBypassMark() passed but marked dial failed: %v", dialErr)
	}
	if verifyErr != nil && dialErr == nil {
		t.Fatalf("VerifyBypassMark() = %v but marked dial succeeded", verifyErr)
	}
}

func TestSocketProtectorRunsWhenMarkIsZero(t *testing.T) {
	original := BypassMark()
	t.Cleanup(func() {
		SetBypassMark(original)
		SetSocketProtector(nil)
	})
	SetBypassMark(0)

	var calls atomic.Int32
	SetSocketProtector(func(fd int) error {
		if fd < 0 {
			return errors.New("negative fd")
		}
		calls.Add(1)
		return nil
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	conn, err := NewOutboundDialer(3*time.Second).Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial with protector and mark 0: %v", err)
	}
	_ = conn.Close()
	if calls.Load() == 0 {
		t.Fatal("socket protector was not invoked")
	}
}

func TestSocketProtectorErrorFailsDial(t *testing.T) {
	original := BypassMark()
	t.Cleanup(func() {
		SetBypassMark(original)
		SetSocketProtector(nil)
	})
	SetBypassMark(0)
	SetSocketProtector(func(int) error { return errors.New("denied") })

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	_, err = NewOutboundDialer(3*time.Second).Dial("tcp", listener.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "protect outbound socket") {
		t.Fatalf("dial error = %v, want protector failure", err)
	}
}

func TestSetSocketProtectorIsConcurrentWithDial(t *testing.T) {
	original := BypassMark()
	t.Cleanup(func() {
		SetBypassMark(original)
		SetSocketProtector(nil)
	})
	SetBypassMark(0)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	stop := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		var n atomic.Int32
		for {
			select {
			case <-stop:
				return
			default:
				current := n.Add(1)
				SetSocketProtector(func(int) error {
					if current < 0 {
						return errors.New("unreachable")
					}
					return nil
				})
			}
		}
	}()

	for range 20 {
		conn, err := NewOutboundDialer(3*time.Second).Dial("tcp", listener.Addr().String())
		if err != nil {
			close(stop)
			workers.Wait()
			t.Fatalf("dial during protector swap: %v", err)
		}
		_ = conn.Close()
	}
	close(stop)
	workers.Wait()
}

func TestFixedResolverConfiguration(t *testing.T) {
	resolver := NewFixedResolver("127.0.0.1:53", time.Second)
	if resolver == nil || !resolver.PreferGo || resolver.Dial == nil {
		t.Fatalf("NewFixedResolver() = %+v", resolver)
	}
}
