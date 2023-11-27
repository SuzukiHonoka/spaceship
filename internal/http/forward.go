package http

import (
	"bufio"
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
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const snifferSize = 4 * 1024
const sessionTimeout = 30 * time.Minute

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
		return "", "", "", 0, transport.ErrorBadRequest
	}
	//log.Println(method, targetRawUri)
	switch method {
	case http.MethodConnect:
		// no scheme
		// CONNECT www.google.com:443 HTTP/1.1
		host, port, err = utils.SplitHostPort(targetRawUri)
	default:
		// parse URL from raw
		var targetUrl *url.URL
		targetUrl, err = url.Parse(targetRawUri)
		// if not a legal url format
		if err != nil {
			return method, "", "", 0, err
		}
		// mark
		hasScheme := targetUrl.Scheme != ""
		// divide the host and port
		// this will raise error if port not found
		// 1. http://google.com 2. google.com
		if host, port, err = utils.SplitHostPort(targetUrl.Host); err != nil {
			// other error
			var addrErr *net.AddrError
			errors.As(err, &addrErr)
			if addrErr.Err != "missing port in address" {
				return method, "", "", 0, err
			}
			host = targetUrl.Host
			if hasScheme {
				if v, ok := ProtocolMap[targetUrl.Scheme]; ok {
					port = v
				} else {
					err = fmt.Errorf("unkown scheme: %s %w", targetUrl.Scheme, transport.ErrorBadRequest)
					return method, host, "", 0, err
				}
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
	for _, seg := range []string{method, rawParams} {
		sb.WriteString(seg)
		sb.WriteRune(' ')
	}
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

func (f *Forwarder) forward(reuse chan<- struct{}) error {
	defer close(reuse)
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
		if err = scanner.Err(); err != nil {
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
	// unpack ipv6
	if host[0] == '[' && host[len(host)-1] == ']' {
		host = host[1 : len(host)-1]
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
			if keepAlive = errors.Is(s, transport.ErrorKeepAliveNeeded); !keepAlive {
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
	route, err := router.GetRoute(host)
	if err != nil {
		log.Printf("http: get route for [%s] error: %v", host, err)
		if _, err = f.Conn.Write(MessageServiceUnavailable); err != nil {
			return fmt.Errorf("failed to send reply: %w", err)
		}
		return nil
	}
	log.Printf("http: %s -> %s", net.JoinHostPort(host, strconv.Itoa(int(port))), route)
	r, w := io.Pipe()
	defer utils.ForceCloseAll(w, r)
	// channel for receive err and wait for
	request := transport.NewRequest(host, port)
	proxyError := make(chan error)
	go func() {
		proxyError <- route.Proxy(context.Background(), request, localAddr, f.Conn, r)
	}()
	internalError := make(chan error)
	go func() {
		// buffer rewrite -> reconstructed tcp raw msg
		if b := f.b.Bytes(); len(b) > 0 {
			if _, err := w.Write(f.b.Bytes()); err != nil {
				internalError <- fmt.Errorf("write buffer err: %w", err)
				return
			}
		}
		//log.Println("src -> target start")
		// todo: use our own io copy function with custom buffer and error returning
		if _, err := utils.CopyBuffer(w, f.Conn, nil); err != nil {
			internalError <- fmt.Errorf("%s: %w", "copy stream error", err)
			return
		}
		// client close
		close(internalError)
		//log.Println("src -> target done")
	}()
	//log.Println("wait for local addr")
	//ld := <-localAddr
	//log.Printf("local addr: %s", ld)
	var b bytes.Buffer
	if addr, ok := <-localAddr; !ok || addr == "" {
		b.Write(MessageServiceUnavailable)
	} else if method == "CONNECT" {
		b.Write(MessageConnectionEstablished)
	}
	// message end
	b.WriteString(CRLF)
	if _, err = f.Conn.Write(b.Bytes()); err != nil {
		return fmt.Errorf("send http status error: %w", err)
	}
	select {
	case err = <-proxyError:
		// notify proxy session is ended
		// todo: rpc only check server and client stream copy error
		if err != nil {
			return err
		}
	case err, ok := <-internalError:
		if ok && err != nil {
			return err
		}
	}
	if keepAlive {
		log.Println("http: keep alive")
		reuse <- struct{}{}
	}
	return nil
}
