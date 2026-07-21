package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SuzukiHonoka/spaceship/v2/internal/router"
	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
	"golang.org/x/sync/errgroup"
)

// Config is used to set up and configure a Server
type Config struct {
	// If provided, username/password authentication is enabled,
	// by appending a UserPassAuthenticator to AuthMethods. If not provided,
	// and AUthMethods is nil, then "auth-less" mode is enabled.
	Credentials StaticCredentials
}

type Server struct {
	ctx       context.Context
	config    *Config
	srv       *http.Server
	closeOnce sync.Once
}

func New(ctx context.Context, cfg *Config) *Server {
	return &Server{
		ctx:    ctx,
		config: cfg,
	}
}

func (s *Server) Close() (err error) {
	if s.srv == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		log.Println("http: shutting down")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err = s.srv.Shutdown(ctx)
	})
	return err
}

func (s *Server) ListenAndServe(_, addr string) error {
	log.Printf("http: listening at %s", addr)
	handlerFunc := func() http.Handler {
		if len(s.config.Credentials) > 0 {
			return s.proxyAuth(http.HandlerFunc(s.Handle))
		}
		return http.HandlerFunc(s.Handle)
	}()

	s.srv = &http.Server{
		Addr:              addr,
		Handler:           handlerFunc,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Create error channel for server errors
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- s.srv.ListenAndServe()
	}()

	// Wait for context done or server error
	select {
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			if s.ctx.Err() != nil {
				return s.ctx.Err()
			}
			return nil
		}
		return err
	case <-s.ctx.Done():
		utils.Close(s)
		return s.ctx.Err()
	}
}

// proxyAuth middleware for HTTP proxy authentication
func (s *Server) proxyAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract credentials from the Proxy-Authorization header
		proxyAuth := r.Header.Get("Proxy-Authorization")
		if proxyAuth == "" {
			// No auth provided, request authentication
			w.Header().Set("Proxy-Authenticate", `Basic realm="Proxy"`)
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}

		// We'd need to manually parse the Proxy-Authorization header here
		user, pass, ok := parseBasicAuth(proxyAuth)
		if !ok || !s.config.Credentials.Valid([]byte(user), []byte(pass)) {
			w.Header().Set("Proxy-Authenticate", `Basic realm="Proxy"`)
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}

		// Authentication successful, continue to the actual proxy handling
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Handle(w http.ResponseWriter, r *http.Request) {
	// filter bad request
	if r.URL.Host == "" {
		http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}

	if r.Method == http.MethodConnect {
		s.handleConnect(w, r)
		return
	}
	s.handleRequest(w, r)
}

func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	// build remote addr before routing so route matching sees the bare hostname,
	// not "host:port".
	host, addr, err := BuildRemoteAddr(r)
	if err != nil {
		ServeProxyError(w, r.Host, fmt.Errorf("build remote addr: %w", err))
		return
	}

	// get route for host
	route, err := router.GetRoute(host)
	if err != nil {
		ServeProxyError(w, host, fmt.Errorf("no route: %w", err))
		return
	}
	defer utils.Close(route)

	log.Printf("http: %q -> %s", r.Host, route)

	// hijack the connection
	hj, ok := w.(http.Hijacker)
	if !ok {
		ServeProxyError(w, r.Host, errors.New("hijack not supported"))
		return
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("Expect")), "100-continue") {
		// Hijack disables net/http's automatic 100 Continue response. Send it
		// while the ResponseWriter is still valid so clients waiting before
		// uploading the request body can make progress.
		w.WriteHeader(http.StatusContinue)
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		ServeProxyError(w, r.Host, fmt.Errorf("hijack: %w", err))
		return
	}
	defer utils.Close(conn)

	// actual proxy

	pr, pw := io.Pipe()
	defer utils.Close(pw)

	errGroup, ctx := errgroup.WithContext(s.ctx)

	proxyLocalAddr := make(chan string)
	proxyDone := make(chan struct{})
	errGroup.Go(func() error {
		defer close(proxyDone)
		err := route.Proxy(ctx, addr, proxyLocalAddr, conn, pr)
		// Unblock the copy goroutine on both code paths it can be stuck in:
		//
		// 1. Stuck in pw.Write(): closing pr (the pipe reader) causes the next
		//    pw.Write() to return io.ErrClosedPipe immediately, regardless of
		//    whether data arrived between the conn.Read() and pw.Write() calls.
		//
		// 2. Stuck in conn.Read(): setting a past deadline on the hijacked
		//    connection causes the blocked Read to return a timeout error.
		//
		// Order matters — close the pipe first so that if conn.Read() returns
		// data before the deadline kicks in, the subsequent pw.Write() still
		// unblocks (rather than blocking on a now-unread pipe).
		pr.Close()                   //nolint:errcheck
		conn.SetDeadline(time.Now()) //nolint:errcheck
		return err
	})

	errGroup.Go(func() error {
		// wait for proxy handshake
		localAddr, ok := <-proxyLocalAddr
		if !ok || localAddr == "" {
			return transport.ErrProxyHandshakeFailed
		}
		return nil
	})

	// Forward exactly the request parsed by net/http. Do not copy raw bytes from
	// conn: those bytes may contain pipelined requests which must not bypass
	// authentication, routing, and hop-header filtering.
	errGroup.Go(func() error {
		if err := writeForwardRequest(pw, r, host); err != nil {
			_ = pw.CloseWithError(err)
			return fmt.Errorf("send request failed: %w", err)
		}

		// Keep the transport's upload side open until the response completes.
		// Some proxy transports cannot expose TCP half-close. The forwarded
		// Connection: close header still makes the origin terminate the response,
		// while this wait prevents a fallback full-close from truncating it.
		select {
		case <-proxyDone:
		case <-ctx.Done():
		}
		_ = pw.Close()
		return nil
	})

	if err = errGroup.Wait(); err != nil {
		ServeProxyError(conn, r.Host, err)
	}
}

func writeForwardRequest(w io.Writer, r *http.Request, host string) error {
	forward := r.Clone(r.Context())
	forward.RequestURI = ""
	forward.Header = r.Header.Clone()
	hopHeaders.RemoveHopHeaders(forward.Header)
	if strings.EqualFold(strings.TrimSpace(forward.Header.Get("Expect")), "100-continue") {
		// net/http sends the client its 100 Continue when Request.Body is read.
		// Do not ask the origin to emit a duplicate interim response.
		forward.Header.Del("Expect")
	}

	// Request.Write uses URL.RequestURI for origin-form requests. Clearing the
	// proxy URL authority prevents the absolute-form target from reaching the
	// origin server while retaining the path and query string.
	forward.URL.Scheme = ""
	forward.URL.Host = ""
	if forward.Host == "" {
		forward.Host = host
	}

	// The handler owns one parsed request. Closing the origin connection after
	// its response prevents later keep-alive/pipelined requests from bypassing
	// the HTTP handler on the already-hijacked client connection.
	forward.Close = true
	forward.Header.Set("Connection", "close")

	// Request.Write re-encodes Body according to ContentLength,
	// TransferEncoding, and Trailer, preserving valid HTTP framing.
	return forward.Write(w)
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		ServeProxyError(w, r.Host, fmt.Errorf("invalid host: %w", err))
		return
	}

	// get route for host
	route, err := router.GetRoute(host)
	if err != nil {
		ServeProxyError(w, r.Host, fmt.Errorf("no route: %w", err))
		return
	}
	defer utils.Close(route)

	log.Printf("http: CONNECT %q -> %s", r.Host, route)

	// hijack the connection
	hj, ok := w.(http.Hijacker)
	if !ok {
		ServeProxyError(w, r.Host, errors.New("hijack not supported"))
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		ServeProxyError(w, r.Host, fmt.Errorf("hijack: %w", err))
		return
	}
	defer utils.Close(conn)

	// build remote addr
	_, addr, err := BuildRemoteAddr(r)
	if err != nil {
		ServeProxyError(conn, r.Host, fmt.Errorf("build remote addr: %w", err))
		return
	}

	// actual proxy
	errGroup, ctx := errgroup.WithContext(s.ctx)

	proxyLocalAddr := make(chan string)
	errGroup.Go(func() error {
		return route.Proxy(ctx, addr, proxyLocalAddr, conn, conn)
	})

	errGroup.Go(func() error {
		// wait for proxy handshake
		localAddr, ok := <-proxyLocalAddr
		if !ok || localAddr == "" {
			return transport.ErrProxyHandshakeFailed
		}

		// send proxy OK
		if _, err = conn.Write(MessageConnectionEstablished); err != nil {
			return fmt.Errorf("send connection established failed: %w", err)
		}

		return nil
	})

	// wait for proxy to finish
	if err = errGroup.Wait(); err != nil {
		ServeProxyError(conn, r.Host, err)
	}
}
