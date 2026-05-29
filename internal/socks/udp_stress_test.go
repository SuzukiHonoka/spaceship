package socks

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
)

func TestUDPRelay_Stress(t *testing.T) {
	// Initialize a dummy router that uses direct transport.
	router.SetRoutes(router.Routes{
		&router.Route{
			MatchType:   router.TypeRegex,
			Sources:     []string{".*"},
			Destination: router.EgressDirect,
		},
	})
	router.GenerateCache()

	// Create 100 echo servers to act as different targets.
	numTargets := 100
	numPackets := 20
	
	echoServers := make([]net.PacketConn, numTargets)
	for i := 0; i < numTargets; i++ {
		es, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("failed to start echo server: %v", err)
		}
		echoServers[i] = es
		go func(conn net.PacketConn) {
			buf := make([]byte, 2048)
			for {
				n, addr, err := conn.ReadFrom(buf)
				if err != nil {
					return
				}
				_, _ = conn.WriteTo(buf[:n], addr)
			}
		}(es)
	}

	relay, err := NewUDPRelay(nil)
	if err != nil {
		t.Fatalf("failed to create udp relay: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = ctx

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = relay.Run()
	}()

	var targetWg sync.WaitGroup
	targetWg.Add(numTargets)

	for i := 0; i < numTargets; i++ {
		go func(targetID int) {
			defer targetWg.Done()
			
			client, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Errorf("failed to start client: %v", err)
				return
			}
			defer client.Close()
			
			if uc, ok := client.(*net.UDPConn); ok {
				_ = uc.SetReadBuffer(1024 * 1024)
			}
			
			es := echoServers[targetID]
			
			addrSpec := &AddrSpec{
				IP:   net.ParseIP("127.0.0.1"),
				Port: uint16(es.LocalAddr().(*net.UDPAddr).Port),
			}
			header, _ := MarshalUDPHeader(addrSpec)

			for p := 0; p < numPackets; p++ {
				payload := []byte("hello from client to target")
				packet := make([]byte, len(header)+len(payload))
				copy(packet, header)
				copy(packet[len(header):], payload)

				relayUDPAddr := relay.RelayAddr().(*net.UDPAddr)
				destAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: relayUDPAddr.Port}
				_, err = client.WriteTo(packet, destAddr)
				if err != nil {
					t.Errorf("target %d failed to write: %v", targetID, err)
					return
				}

				_ = client.SetReadDeadline(time.Now().Add(10 * time.Second))
				buf := make([]byte, 2048)
				n, _, err := client.ReadFrom(buf)
				if err != nil {
					t.Errorf("target %d failed to read: %v", targetID, err)
					return
				}

				// Verify it's not empty
				if n <= len(header) {
					t.Errorf("target %d received truncated packet", targetID)
				}
			}
		}(i)
	}

	targetWg.Wait()

	// Shutdown everything
	_ = relay.Close()
	for _, es := range echoServers {
		_ = es.Close()
	}
	wg.Wait()
}
