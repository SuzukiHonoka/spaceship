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
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	rpcClient "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/client"
)

// Server timeouts guard against slow-client (Slowloris) resource exhaustion.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 10 * time.Second
	writeTimeout      = 10 * time.Second
	idleTimeout       = 60 * time.Second
	shutdownTimeout   = 5 * time.Second
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

// ipIsLoopback reports whether a "host:port" (or bare "host") string refers to a
// loopback IP literal. It correctly handles IPv6 ("[::1]:port") via SplitHostPort.
// Hostnames (including "localhost") are not accepted — use hostHeaderAllowed for
// the client-supplied Host header, where "localhost" is permitted for usability.
func ipIsLoopback(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// hostHeaderAllowed validates the client-supplied Host header to prevent
// DNS-rebinding attacks. It accepts loopback IP literals and the literal
// "localhost" (which a browser cannot forge into a cross-origin request).
func hostHeaderAllowed(host string) bool {
	h := host
	if hh, _, err := net.SplitHostPort(host); err == nil {
		h = hh
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// loopbackGuard rejects any request that does not originate from loopback or
// carries a non-loopback Host header. Applied uniformly to every endpoint as a
// defense-in-depth layer on top of the loopback-only listener bind.
func loopbackGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ipIsLoopback(r.RemoteAddr) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !hostHeaderAllowed(r.Host) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handler builds the management HTTP handler with all routes and the loopback guard.
func handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/stats", handleStats)
	mux.HandleFunc("/api/health", handleHealth)
	return loopbackGuard(mux)
}

// Start starts the HTTP management server on addr (must be a loopback address).
// It blocks until ctx is canceled or a fatal listen error occurs.
func Start(ctx context.Context, addr string) error {
	if !ipIsLoopback(addr) {
		return fmt.Errorf("management server must be bound to a loopback address, got: %s", addr)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("management server listen: %w", err)
	}
	log.Printf("management server listening on http://%s", ln.Addr())
	return serve(ctx, ln)
}

// serve runs the management server on an existing listener until ctx is canceled.
func serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}

	// Stop the server when the context is canceled.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("management server shutdown error: %v", err)
		}
	}()

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("management server error: %w", err)
	}
	return nil
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
