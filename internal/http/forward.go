package http

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/transport/router"
	"github.com/SuzukiHonoka/spaceship/internal/util"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const snifferSize = 4 * 1024
const sessionTimeout = 3 * time.Minute

type Forwarder struct {
	Ctx  context.Context
	Conn net.Conn
	b    *bytes.Buffer
}

// ParseReqFromRaw parses request from raw tcp message
func ParseReqFromRaw(target string) (method, host, params string, port uint16, err error) {
	method, rest, ok1 := strings.Cut(target, " ")
	targetRawUri, _, ok2 := strings.Cut(rest, " ")
	// proper request format at first line: (HTTP_METHOD TARGET_URL HTTP_VERSION)
	// -> GET https://www.google.com HTTP/1.1
	// it should have 3 elements divided by space
	if !ok1 || !ok2 {
		return method, host, params, port, transport.ErrorBadRequest
	}
	//log.Println(method, targetRawUri)
	switch method {
	case http.MethodConnect:
		// no scheme
		// CONNECT www.google.com:443 HTTP/1.1
		host, port, err = transport.SplitHostPort(targetRawUri)
	default:
		// parse URL from raw
		var targetUrl *url.URL
		targetUrl, err = url.Parse(targetRawUri)
		// if not a legal url format
		if err != nil {
			return method, host, params, port, err
		}
		// mark
		hasScheme := targetUrl.Scheme != ""
		// divide the host and port
		host, port, err = transport.SplitHostPort(targetUrl.Host)
		// will raise error if port not found
		// 1. http://google.com 2. google.com
		if err != nil {
			// other error
			if strings.LastIndex(err.Error(), "missing port in address") == -1 {
				return method, host, params, port, err
			}
			host = targetUrl.Host
			if hasScheme {
				v, ok := ProtocolMap[targetUrl.Scheme]
				if !ok {
					err = fmt.Errorf("unkown scheme: %s %w", targetUrl.Scheme, transport.ErrorBadRequest)
					return method, host, params, port, err
				}
				port = v
			} else {
				port = 80
			}
		}
		params, err = GetRawParamsFromUrl(hasScheme, targetRawUri)
	}
	//log.Println("req parsed:", method, host, params, port)
	return method, host, params, port, nil
}

func (f *Forwarder) handleProxy(method, rawParams string, reader *bytes.Reader, scanner *bufio.Scanner) (err error) {
	var sb strings.Builder
	sb.WriteString(method)
	sb.WriteRune(' ')
	sb.WriteString(rawParams)
	sb.WriteRune(' ')
	sb.WriteString("HTTP/1.1")
	sb.WriteString(CRLF)
	head := sb.String()
	//log.Printf("head: %s", head)
	_, _ = f.b.WriteString(head)
	// filter headers
	for scanner.Scan() {
		line := scanner.Text()
		// if headers end
		if line == "" {
			// if no payload
			if !scanner.Scan() {
				f.b.WriteString(CRLF)
				return nil
			}
			// payload exist -> write back
			f.b.Write(scanner.Bytes())
			break
		}
		//log.Println(line)
		s := strings.Index(line, ":")
		headerName := strings.ToLower(line[:s])
		//v := strings.ToLower(line[s+1:])
		//log.Printf("http.parsed: [%s]: [%s]", headerName, v)
		if headerName == "proxy-connection" && strings.TrimSpace(strings.ToLower(line[s+1:])) == "keep-alive" {
			err = transport.ErrorKeepAliveNeeded
			//log.Println("http: keep alive needed")
		}
		if !hopHeaders.Filter(headerName) {
			sb = strings.Builder{}
			sb.WriteString(line)
			sb.WriteString(CRLF)
			f.b.WriteString(sb.String())
		}
	}
	// rest of raw data
	_, _ = reader.WriteTo(f.b)
	return err
}

func (f *Forwarder) handleTunnel(reader *bytes.Reader, scanner *bufio.Scanner) (err error) {
	// ignore the headers
	for scanner.Scan() {
		if scanner.Text() == "" {
			break
		}
	}
	// rests of raw data
	_, _ = reader.WriteTo(f.b)
	return nil
}

func GetRawParamsFromUrl(scheme bool, url string) (string, error) {
	if scheme {
		// get params
		// with scheme -> http://host/params...
		count, i := 0, 0
		for ; count < 3 && i < len(url); i++ {
			// ascii code of "/" is 47
			if url[i] == 47 {
				count++
			}
		}
		if count != 3 {
			return "", errors.New("delimiter not found")
		}
		return url[i-1:], nil
	}
	// get params
	// without scheme -> host/params...
	i := strings.IndexByte(url, '/')
	if i == -1 {
		return "", errors.New("delimiter not found")
	}
	return url[i:], nil
}

func (f *Forwarder) Forward() error {
	proxyError := make(chan error)
	// MAX_REUSE_COUNT = 32
	for i := 0; i < 32; i++ {
		observer := make(chan struct{})
		go func() {
			proxyError <- f.forward(observer)
		}()
		t := time.NewTimer(sessionTimeout)
		select {
		case <-t.C:
			return os.ErrDeadlineExceeded
		case err := <-proxyError:
			t.Stop()
			// normal end if nil
			if err != nil {
				// internal error: connection down, etc.
				//if err != io.EOF || errors.Is(err, net.ErrClosed) {
				//	log.Printf("http: end due error: %v", err)
				//}
				return err
			}
			// wait for signal of observer
			t.Reset(sessionTimeout)
			select {
			case <-t.C:
				//log.Println("http: rpc timed out")
				return os.ErrDeadlineExceeded
			case _, ok := <-observer:
				t.Stop()
				if !ok {
					//log.Println("http: not reuse")
					return nil
				}
				log.Println("http: reuse")
				// session end but connection still present
				continue
				//case _, ok := <-observer:
				//	if !ok {
				//		//log.Println("http: not reuse")
				//		break
				//	}
				//	log.Println("http: reuse")
				//	// session end but connection still present
				//	continue
			}
		}
	}
	return nil
}

func (f *Forwarder) forward(notify chan<- struct{}) error {
	defer close(notify)
	// 4k buffer, capable of storing up to 2048 words which enough for http headers
	// used for store raw socket messages to identify the remote host and filter sensitive-http-headers
	tmp := make([]byte, snifferSize)
	// actual reads to the buffer
	n, err := f.Conn.Read(tmp)
	if err != nil {
		return err
	}
	// read first line to check if the format is legal and switch transport filter manners
	// note that HTTP CONNECT is direct tunnel
	reader := bytes.NewReader(tmp[:n])
	scanner := bufio.NewScanner(reader)
	// scan the first line, if we reached the end or any error occurred
	if ok := scanner.Scan(); !ok {
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("scann first line failed: %w", err)
		}
		return transport.ErrorBadRequest
	}
	// match the raw parameter
	// only in formal request methods and having http prefix in URL
	first := scanner.Text()
	method, host, params, port, err := ParseReqFromRaw(first)
	// parse error
	if err != nil {
		return fmt.Errorf("parse request failed: %w", err)
	}
	// buffer for stored raw messages
	// len:0 max-cap:4k
	f.b = bytes.NewBuffer(make([]byte, 0, snifferSize))
	// keepalive
	var keepAlive bool
	// check request method
	switch method {
	case http.MethodConnect:
		err = f.handleTunnel(reader, scanner)
	default:
		if s := f.handleProxy(method, params, reader, scanner); s != nil {
			if keepAlive = s == transport.ErrorKeepAliveNeeded; !keepAlive {
				err = s
			}
		}
	}
	// handle error
	if err != nil {
		return fmt.Errorf("handle request failed: %w", err)
	}
	//forward process
	localAddr := make(chan string)
	ctx, done := context.WithCancel(f.Ctx)
	defer done()
	valuedCtx := context.WithValue(ctx, "request", &transport.Request{
		Fqdn: host,
		Port: port,
	})
	route := router.RoutesCache.GetRoute(host)
	if route == nil {
		_, _ = f.Conn.Write([]byte("HTTP/1.1 503 Service Unavailable" + CRLF))
		return nil
	}
	log.Printf("http: %s -> %s", net.JoinHostPort(host, strconv.Itoa(int(port))), route)
	r, w := io.Pipe()
	defer func() {
		_ = w.Close()
		_ = r.Close()
	}()
	// channel for receive err and wait for
	proxyError := make(chan error)
	go func() {
		err := route.Proxy(valuedCtx, localAddr, f.Conn, r)
		proxyError <- err
	}()
	internalError := make(chan error)
	go func() {
		// buffer rewrite -> reconstructed tcp raw msg
		if b := f.b.Bytes(); len(b) > 0 {
			if _, err := w.Write(f.b.Bytes()); err != nil {
				internalError <- fmt.Errorf("write buffer err: %w", err)
			}
		}
		//log.Println("src -> target start")
		// todo: use our own io copy function with custom buffer and error returning
		if _, err := util.CopyBuffer(w, f.Conn, nil); err != nil {
			internalError <- fmt.Errorf("%s: %w", "copy stream error", err)
		}
		// client close
		internalError <- io.EOF
		//log.Println("src -> target done")
	}()
	//log.Println("wait for local addr")
	//ld := <-localAddr
	//log.Printf("local addr: %s", ld)
	var b bytes.Buffer
	if <-localAddr == "" {
		b.WriteString("HTTP/1.1 503 Service Unavailable")
	} else if method == "CONNECT" {
		b.WriteString("HTTP/1.1 200 Connection established")
	}
	// message end
	for i := 0; i < 2; i++ {
		b.WriteString(CRLF)
	}
	if _, err = f.Conn.Write(b.Bytes()); err != nil {
		return fmt.Errorf("send http status error: %w", err)
	}
	select {
	case err := <-proxyError:
		// notify proxy session is ended
		// todo: rpc only check server and client stream copy error
		if err != nil {
			transport.PrintErrorIfCritical(err, "http")
		} else if keepAlive {
			//log.Println("keep alive")
			notify <- struct{}{}
		}
	case err := <-internalError:
		return err
	}
	return nil
}
