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
	"strconv"
	"strings"
)

type Forwarder struct {
	Ctx context.Context
	transport.Transport
	Conn net.Conn
	b    *bytes.Buffer
}

func ParseReqFromRaw(target string) (method, host, params string, port int, err error) {
	method, rest, ok1 := strings.Cut(target, " ")
	targetRawUri, _, ok2 := strings.Cut(rest, " ")
	// proper request format at first line: (HTTP_METHOD TARGET_URL HTTP_VERSION)
	// -> GET https://www.google.com HTTP/1.1
	// it should have 3 elements divided by space
	if !ok1 || !ok2 {
		return method, host, params, port, transport.ErrorBadRequest
	}
	//log.Println(method, targetRawUri)
	var sport string
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
				switch targetUrl.Scheme {
				case "http":
					sport = "80"
				case "https":
					sport = "443"
				default:
					err = fmt.Errorf("unkown scheme: %s %w", targetUrl.Scheme, transport.ErrorBadRequest)
					return method, host, params, port, err
				}
			} else {
				sport = "80"
			}
		}
		params = GetRawParamsFromUrl(hasScheme, targetRawUri)
	}
	port, err = strconv.Atoi(sport)
	//log.Println("req parsed:", method, host, params, port)
	return method, host, params, port, nil
}

func (f *Forwarder) handleProxy(method, rawParams string, reader *bytes.Reader, scanner *bufio.Scanner) error {
	head := fmt.Sprintf("%s %s %s\n", method, rawParams, "HTTP/1.1")
	//log.Printf("head: %s", head)
	_, _ = f.b.WriteString(head)
	// filter headers
	for scanner.Scan() {
		line := scanner.Text()
		// if headers end
		if line == "" {
			// if no payload
			if !scanner.Scan() {
				f.b.WriteString("\r\n")
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
			return err
		}
		return transport.ErrorBadRequest
	}
	// match the raw parameter
	// only in formal request methods and having http prefix in URL
	first := scanner.Text()
	method, host, params, port, err := ParseReqFromRaw(first)
	if err != nil {
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
	// parse error
	if err != nil {
		return err
	}
	log.Printf("http: %s:%d -> rpc", host, port)
	//forward process
	localAddr := make(chan string)
	valuedCtx := context.WithValue(f.Ctx, "request", &transport.Request{
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
				log.Println("write err:", err)
				proxyError <- err
			}
		}
		//log.Println("src -> target start")
		_, err = io.Copy(w, f.Conn)
		if err != nil {
			log.Println("copy err:", err)
			proxyError <- err
		}
		proxyError <- io.EOF
		//log.Println("src -> target done")
	}()
	//log.Println("wait for local addr")
	//ld := <-localAddr
	//log.Printf("local addr: %s", ld)
	if <-localAddr == "" {
		_, _ = f.Conn.Write([]byte("HTTP/1.1 503 Service Unavailable\r\n\r\n"))
	} else if method == "CONNECT" {
		_, err = f.Conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		if err != nil {
			return err
		}
	}
	return <-proxyError
}
