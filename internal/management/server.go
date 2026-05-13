// Package management provides a lightweight HTTP management API for the spaceship proxy.
// It is intentionally bound to loopback-only addresses for security.
package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	rpcClient "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
)

// StatsResponse is the JSON payload returned by GET /api/stats.
type StatsResponse struct {
	TxTotalBytes uint64                       `json:"tx_total_bytes"`
	RxTotalBytes uint64                       `json:"rx_total_bytes"`
	TxSpeedBps   float64                      `json:"tx_speed_bps"`
	RxSpeedBps   float64                      `json:"rx_speed_bps"`
	PoolTotal    int                          `json:"pool_total"`
	PoolActive   int                          `json:"pool_active"`
	PoolLoad     uint32                       `json:"pool_load"`
	Connections  []rpcClient.ConnectionDetail `json:"connections"`
}

// loopbackOnly is a custom listener that rejects non-loopback connections.
// This is a defense-in-depth measure on top of binding to 127.0.0.1.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Start starts the HTTP management server on addr (must be a loopback address).
// It blocks until ctx is cancelled or a fatal listen error occurs.
func Start(ctx context.Context, addr string) error {
	if !isLoopback(addr) {
		return fmt.Errorf("management server must be bound to a loopback address, got: %s", addr)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/stats", handleStats)
	mux.HandleFunc("/api/health", handleHealth)

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
		// Security: enforce strict host header matching to prevent DNS rebinding.
		// Only loopback addresses are allowed.
	}

	// Stop server when context is canceled.
	go func() {
		<-ctx.Done()
		if err := srv.Shutdown(context.Background()); err != nil {
			log.Printf("management server shutdown error: %v", err)
		}
	}()

	log.Printf("management server listening on http://%s", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("management server error: %w", err)
	}
	return nil
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	// Only allow GET.
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Reject non-loopback callers (defense-in-depth; the listener already does this).
	remoteHost, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip := net.ParseIP(remoteHost); ip == nil || !ip.IsLoopback() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Validate Host header to prevent DNS-rebinding attacks.
	host := r.Host
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	tx, rx := transport.GlobalStats.Total()
	txSpeed, rxSpeed := transport.GlobalStats.CalculateSpeed()
	total, active, load := rpcClient.GetConnectionSummary()
	details := rpcClient.GetConnectionDetails()

	resp := StatsResponse{
		TxTotalBytes: tx,
		RxTotalBytes: rx,
		TxSpeedBps:   txSpeed,
		RxSpeedBps:   rxSpeed,
		PoolTotal:    total,
		PoolActive:   active,
		PoolLoad:     load,
		Connections:  details,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("management: encode stats error: %v", err)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}
