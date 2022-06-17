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
	}
	// match the raw parameter
	// only in formal request methods and having http prefix in URL
	first := scanner.Text()
	sequence := strings.Split(first, " ")
	// proper request format at first line: (HTTP_METHOD TARGET_URL HTTP_VERSION)
	// -> GET https://www.google.com HTTP/1.1
	// it should have 3 elements divided by space
	if len(sequence) != 3 {
		return transport.ErrorBadRequest
	}
	// buffer for stored raw messages
	// len:0 max-cap:4k
	b := bytes.NewBuffer(make([]byte, 0, 4*1024))
	// check request method
	var host, sport string
	switch sequence[0] {
	case "CONNECT":
		host, sport, err = net.SplitHostPort(sequence[1])
		// ignore the headers
		for scanner.Scan() {
			if scanner.Text() == "" {
				break
			}
		}
		// rests of raw data
		_, _ = reader.WriteTo(b)
	default:
		// get remote target url
		targetRawUrl := sequence[1]
		// parse URL from raw
		targetUrl, err := url.Parse(targetRawUrl)
		if err != nil {
			return err
		}
		hasScheme := targetUrl.Scheme != ""
		host, sport, err = net.SplitHostPort(targetUrl.Host)
		// port not found or other error occurred
		if err != nil {
			// get port by scheme
			if strings.LastIndex(err.Error(), "missing port in address") != -1 && hasScheme {
				host = targetUrl.Host
				// set port by scheme
				switch targetUrl.Scheme {
				case "http":
					sport = "80"
				case "https":
					sport = "443"
				default:
					return fmt.Errorf("unkown scheme: %s %w", targetUrl.Scheme, transport.ErrorBadRequest)
				}
			} else {
				return err
			}
		}
		// write back first header
		// with scheme -> http://host/params...
		// without scheme -> host/params...
		var rawParams string
		if hasScheme {
			var count uint8
			for i := 0; i < len(targetRawUrl); i++ {
				if count == 3 {
					rawParams = targetRawUrl[i:]
					break
				}
				s := targetRawUrl[i]
				// ascii code of "/" is 47
				if s == 47 {
					count++
				}
			}
		} else {
			i := strings.IndexByte(rawParams, '/')
			rawParams = targetRawUrl[i:]
		}
		_, _ = b.WriteString(fmt.Sprintf("%s /%s %s\n", sequence[0], rawParams, sequence[2]))
		// filter headers
		for scanner.Scan() {
			line := scanner.Text()
			// if headers end
			if line == "" {
				break
			}
			//log.Println(line)
			headerName := line[:strings.Index(line, ":")]
			if !hopHeadersMap.Filter(headerName) {
				_, _ = b.WriteString(line + "\n")
			}
		}
		// reset raw data
		_, _ = reader.WriteTo(b)
		b.WriteByte('\n')
	}
	// parse error
	if err != nil {
		return err
	}
	log.Printf("http: %s:%s -> rpc", host, sport)
	//forward process
	port, err := strconv.Atoi(sport)
	localAddr := make(chan string)
	valuedCtx := context.WithValue(f.Ctx, "request", &transport.Request{
		Fqdn: host,
		Port: port,
	})
	r, w := io.Pipe()
	defer func(w *io.PipeWriter) {
		_ = w.Close()
		_ = r.Close()
		_ = f.Close()
	}(w)
	// channel for receive err and wait for
	proxyError := make(chan error)
	go func() {
		err := f.Proxy(valuedCtx, localAddr, f.Conn, r)
		proxyError <- err
	}()
	go func() {
		// buffer rewrite
		_, err = w.Write(b.Bytes())
		if err != nil {
			proxyError <- err
		}
		_, err = io.Copy(w, f.Conn)
		if err != nil {
			proxyError <- err
		}
	}()
	//log.Println("wait for local addr")
	//ld := <-localAddr
	//log.Printf("local addr: %s", ld)
	if <-localAddr != "" {
		if sequence[0] == "CONNECT" {
			_, err = f.Conn.Write([]byte("HTTP/1.1 200 OK\n\n"))
			if err != nil {
				proxyError <- err
			}
		}
		//log.Println("ok sent")
	} else {
		_, err = f.Conn.Write([]byte("HTTP/1.1 500 Internal Server Error\n\n"))
		if err != nil {
			proxyError <- err
		}
	}
	return <-proxyError
}
