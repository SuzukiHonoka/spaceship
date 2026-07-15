package transport

import (
	"sync"
	"testing"
	"time"
)

func TestTransport_GettersAndSetters(t *testing.T) {
	t.Cleanup(EnableIPv6)

	// Test buffer size
	SetBufferSize(64) // KB
	if size := GetBufferSize(); size != 64*1024 {
		t.Errorf("GetBufferSize() = %v, want %v", size, 64*1024)
	}

	// Test network
	SetNetwork("tcp6")
	if net := GetNetwork(); net != "tcp6" {
		t.Errorf("GetNetwork() = %v, want %v", net, "tcp6")
	}

	// Test DisableIPv6
	DisableIPv6()
	if net := GetNetwork(); net != "tcp4" {
		t.Errorf("GetNetwork() after DisableIPv6 = %v, want %v", net, "tcp4")
	}
	if !PreferIPv4() {
		t.Error("PreferIPv4() = false after DisableIPv6, want true")
	}
	if got := DialNetwork("udp"); got != "udp4" {
		t.Errorf("DialNetwork(udp) after DisableIPv6 = %v, want udp4", got)
	}
	if got := DialNetwork("tcp"); got != "tcp4" {
		t.Errorf("DialNetwork(tcp) after DisableIPv6 = %v, want tcp4", got)
	}

	// Test dial timeout
	SetDialTimeout(5 * time.Minute)
	if timeout := GetDialTimeout(); timeout != 5*time.Minute {
		t.Errorf("GetDialTimeout() = %v, want %v", timeout, 5*time.Minute)
	}

	// Test idle timeout
	SetIdleTimeout(10 * time.Minute)
	if timeout := GetIdleTimeout(); timeout != 10*time.Minute {
		t.Errorf("GetIdleTimeout() = %v, want %v", timeout, 10*time.Minute)
	}
}

func TestTransport_Concurrency(t *testing.T) {
	// Ensure that Getters and Setters don't panic under concurrent access
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(val int) {
			defer wg.Done()
			SetBufferSize(uint16(val))
			_ = GetBufferSize()

			SetNetwork("tcp")
			_ = GetNetwork()

			SetDialTimeout(time.Duration(val) * time.Second)
			_ = GetDialTimeout()

			SetIdleTimeout(time.Duration(val) * time.Second)
			_ = GetIdleTimeout()
		}(i)
	}
	wg.Wait()
}
