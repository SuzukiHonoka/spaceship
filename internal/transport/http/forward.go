package http

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"spaceship/internal/transport"
	"strings"
)

type Forwarder struct {
	Ctx context.Context
	transport.Transport
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
	case "CONNECT":
		// no scheme
		// CONNECT www.google.com:443 HTTP/1.1
		host, port, err = transport.SplitHostPort(targetRawUri)
	default:
		// parse URL from raw
		targetUrl, err := url.Parse(targetRawUri)
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
		params = GetRawParamsFromUrl(hasScheme, targetRawUri)
	}
	//log.Println("req parsed:", method, host, params, port)
	return method, host, params, port, nil
}

func (f *Forwarder) handleProxy(method, rawParams string, reader *bytes.Reader, scanner *bufio.Scanner) error {
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
			} else {
				// payload exist -> write back
				f.b.Write(scanner.Bytes())
			}
			break
		}
		//log.Println(line)
		headerName := line[:strings.Index(line, ":")]
		if !hopHeaders.Filter(headerName) {
			sb = strings.Builder{}
			sb.WriteString(line)
			sb.WriteString(CRLF)
			f.b.WriteString(line + CRLF)
		}
	}
	// rest of raw data
	_, _ = reader.WriteTo(f.b)
	return nil
}

func (f *Forwarder) handleTunnel(reader *bytes.Reader, scanner *bufio.Scanner) error {
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

func GetRawParamsFromUrl(scheme bool, url string) (params string) {
	if scheme {
		// get params
		// with scheme -> http://host/params...
		for count, i := 0, 0; i < len(url); i++ {
			s := url[i]
			// ascii code of "/" is 47
			if s == 47 {
				count++
			}
			if count == 3 {
				params = url[i:]
				break
			}
		}
	} else {
		// get params
		// without scheme -> host/params...
		i := strings.IndexByte(url, '/')
		params = url[i:]
	}
	return params
}

func (f *Forwarder) Forward() error {
	// 4k buffer, capable of storing up to 2048 words which enough for http headers
	// used for store raw socket messages to identify the remote host and filter sensitive-http-headers
	tmp := make([]byte, 4*1024)
	// actual reads to the buffer
	n, err := f.Conn.Read(tmp)
	if err != nil {
		return err
	}
	// read first line to check if the format is legal and switch transport filter manners
	// note that HTTP CONNECT is direct tunnel
	reader := bytes.NewReader(tmp[:n])
	scanner := bufio.NewScanner(reader)
	// scan the first line
	ok := scanner.Scan()
	// if we reached the end or any error occurred
	if !ok {
		if err := scanner.Err(); err != nil {
			log.Printf("http: scann first line failed: %s", err)
			return err
		}
		return transport.ErrorBadRequest
	}
	// match the raw parameter
	// only in formal request methods and having http prefix in URL
	first := scanner.Text()
	method, host, params, port, err := ParseReqFromRaw(first)
	// parse error
	if err != nil {
		log.Printf("http: parse request failed: %s", err)
		return err
	}
	// buffer for stored raw messages
	// len:0 max-cap:4k
	f.b = bytes.NewBuffer(make([]byte, 0, 4*1024))
	// check request method
	switch method {
	case "CONNECT":
		err = f.handleTunnel(reader, scanner)
	default:
		err = f.handleProxy(method, params, reader, scanner)
	}
	// handle error
	if err != nil {
		log.Printf("http: handle request failed: %s", err)
		return err
	}
	log.Printf("http: %s:%d -> rpc", host, port)
	//forward process
	localAddr := make(chan string)
	ctx, done := context.WithCancel(f.Ctx)
	defer done()
	valuedCtx := context.WithValue(ctx, "request", &transport.Request{
		Fqdn: host,
		Port: port,
	})
	r, w := io.Pipe()
	defer func() {
		_ = w.Close()
		_ = r.Close()
	}()
	// channel for receive err and wait for
	proxyError := make(chan error)
	go func() {
		err = f.Proxy(valuedCtx, localAddr, f.Conn, r)
		proxyError <- err
	}()
	go func() {
		// buffer rewrite -> reconstructed tcp raw msg
		if b := f.b.Bytes(); len(b) > 0 {
			_, err = w.Write(f.b.Bytes())
			if err != nil {
				log.Println("http: write buffer err:", err)
				proxyError <- err
			}
		}
		//log.Println("src -> target start")
		_, err = io.Copy(w, f.Conn)
		if err != nil {
			transport.PrintErrorIfNotCritical(err, "http: copy stream error")
			proxyError <- err
		}
		proxyError <- io.EOF
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
	_, err = f.Conn.Write(b.Bytes())
	if err != nil {
		transport.PrintErrorIfNotCritical(err, "http: send http status error")
		return err
	}
	return <-proxyError
}
