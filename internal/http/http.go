package http

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/router"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
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
		// Graceful shutdown with timeout
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = s.srv.Shutdown(shutdownCtx)
	})
	return err
}

func (s *Server) ListenAndServe(_, addr string) error {
	log.Printf("http will listen at %s", addr)
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
		return err
	case <-s.ctx.Done():
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
	// get route for host
	route, err := router.GetRoute(r.Host)
	if err != nil {
		ServeError(w, fmt.Errorf("http: get route for %s failed, error=%w", r.Host, err))
		return
	}
	defer utils.Close(route)

	log.Printf("http: %s -> %s", r.Host, route)

	// hijack the connection
	hj, ok := w.(http.Hijacker)
	if !ok {
		ServeError(w, errors.New("webserver doesn't support hijacking"))
		return
	}
	conn, unprocessed, err := hj.Hijack()
	if err != nil {
		ServeError(w, fmt.Errorf("http: hijack connection failed: %w", err))
		return
	}
	defer utils.Close(conn)

	// build remote request
	request, err := BuildRemoteRequest(r)
	if err != nil {
		ServeError(conn, fmt.Errorf("http: build remote request failed: %w", err))
		return
	}

	// actual proxy
	proxyErr := make(chan error)
	proxyLocalAddr := make(chan string)

	pr, pw := io.Pipe()
	defer utils.Close(pw)

	// DEBUG ONLY
	//tpr, tpw := io.Pipe()
	//go func() {
	//	// read 4k bytes per time, and print the raw message
	//	buf := make([]byte, 4096)
	//	for {
	//		n, err := tpr.Read(buf)
	//		if err != nil {
	//			if err != io.EOF {
	//				log.Println("http: read raw message failed")
	//			}
	//			break
	//		}
	//		fmt.Println(string(buf[:n]))
	//	}
	//}()

	proxyCtx, cancel := context.WithCancel(s.ctx)
	defer cancel()

	go func() {
		proxyErr <- route.Proxy(proxyCtx, request, proxyLocalAddr, conn, pr)
	}()

	// wait for proxy handshake
	localAddr, ok := <-proxyLocalAddr
	if !ok || localAddr == "" {
		ServeError(conn, fmt.Errorf("http: proxy handshake failed for %s", r.Host))
		return
	}

	// write tcp raw msg to pipe, construct HTTP message
	// raw head eg:  GET / HTTP/1.1
	buf := new(bytes.Buffer)
	for _, seg := range []string{r.Method, r.URL.Path} {
		buf.WriteString(seg)
		buf.WriteRune(' ')
	}
	buf.WriteString("HTTP/1.1")
	buf.WriteString(CRLF)

	// Host maybe missing in headers, and will cause bad request errors, rewrite it
	if r.Header.Get("Host") == "" {
		r.Header.Set("Host", request.Host)
	}

	// filter sensitive headers
	hopHeaders.RemoveHopHeaders(r.Header)

	// raw headers, should filter sensitive headers
	for k, v := range r.Header {
		buf.WriteString(k)
		buf.WriteRune(':')
		buf.WriteRune(' ')
		buf.WriteString(strings.Join(v, ";")) // in case of multiple values
		buf.WriteString(CRLF)
	}

	// write rest unprocessed body to pipe if any, the body should small, no need to consider performance issue
	if unprocessed.Reader.Buffered() > 0 {
		if _, err = buf.ReadFrom(unprocessed.Reader); err != nil {
			ServeError(conn, fmt.Errorf("http: read unprocessed body failed for %s", r.Host))
			return
		}
	}

	// assume headers field ends
	buf.WriteString(CRLF)

	// DEBUG ONLY
	//msg := string(buf.Bytes())
	//fmt.Println(msg)
	//buf.WriteString(msg)

	// write raw messages to pipe
	if _, err = buf.WriteTo(pw); err != nil {
		ServeError(conn, fmt.Errorf("http: send heads failed for %s, err=%w", r.Host, err))
		return
	}

	// forward the connection
	forwardErr := make(chan error)
	go func() {
		_, err := io.CopyBuffer(pw, conn, transport.AllocateBuffer())
		forwardErr <- err
	}()

	// wait for proxy to finish
	select {
	case <-s.ctx.Done():
		return
	case err = <-forwardErr:
		if err != nil && err != io.EOF {
			ServeError(conn, fmt.Errorf("http: copy body failed for %s， err=%w", r.Host, err))
		}
	case err = <-proxyErr:
		if err != nil {
			ServeError(conn, fmt.Errorf("http: proxy failed, err=%w", err))
		}
	}
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		ServeError(w, fmt.Errorf("invalid host, err=%w", err))
		return
	}

	// get route for host
	route, err := router.GetRoute(host)
	if err != nil {
		ServeError(w, fmt.Errorf("http: get route for %s failed, error=%w", r.Host, err))
		return
	}
	defer utils.Close(route)

	log.Printf("http: CONNECT %s -> %s", r.Host, route)

	// hijack the connection
	hj, ok := w.(http.Hijacker)
	if !ok {
		ServeError(w, errors.New("webserver doesn't support hijacking"))
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		ServeError(w, fmt.Errorf("http: hijack connection failed: %w", err))
		return
	}
	defer utils.Close(conn)

	// build remote request
	request, err := BuildRemoteRequest(r)
	if err != nil {
		ServeError(conn, fmt.Errorf("http: build remote request failed: %w", err))
		return
	}

	// actual proxy
	proxyErr := make(chan error)
	proxyLocalAddr := make(chan string)

	proxyCtx, cancel := context.WithCancel(s.ctx)
	defer cancel()

	go func() {
		proxyErr <- route.Proxy(proxyCtx, request, proxyLocalAddr, conn, conn)
	}()

	// wait for proxy handshake
	localAddr, ok := <-proxyLocalAddr
	if !ok || localAddr == "" {
		ServeError(conn, fmt.Errorf("http: proxy handshake failed for %s", r.Host))
		return
	}

	// send proxy OK
	if _, err = conn.Write(MessageConnectionEstablished); err != nil {
		log.Printf("http: send connection established failed for %s", r.Host)
		return
	}

	// wait for proxy to finish
	select {
	case <-s.ctx.Done():
	case err = <-proxyErr:
		if err != nil {
			ServeError(conn, fmt.Errorf("http: proxy failed: %w", err))
		}
	}
}
