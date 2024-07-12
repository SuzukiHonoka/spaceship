package http

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/router"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	"io"
	"log"
	"net"
	"net/http"
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

func (f *Forwarder) handleProxy(method, rawParams string, reader *bytes.Reader, scanner *bufio.Scanner) (err error) {
	// construct HTTP message
	// head eg:  GET / HTTP/1.1
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

		// filter headers
		k, _, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		headerName := strings.ToLower(k)
		//v := strings.ToLower(line[s+1:])
		//log.Printf("http.parsed: [%s]: [%s]", headerName, v)

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

func (f *Forwarder) Forward() error {
	proxyErr := make(chan error)
	go func() {
		proxyErr <- f.forward()
	}()

	t := time.NewTimer(sessionTimeout)
	select {
	case <-t.C:
		return os.ErrDeadlineExceeded
	case err := <-proxyErr:
		t.Stop()
		if err != nil {
			return fmt.Errorf("http: forward error=%w", err)
		}
	}
	return nil
}

func (f *Forwarder) forward() error {
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
		return transport.ErrBadRequest
	}

	// match the raw parameter
	// only in formal request methods and having http prefix in URL
	first := scanner.Text()
	req, err := ParseRequestFromRaw(first)
	if err != nil {
		return fmt.Errorf("parse request failed: %w", err)
	}

	// unpack ipv6 when necessary
	req.UnpackIPv6()

	// buffer for stored raw messages
	// len:0 max-cap:4k
	f.b = bytes.NewBuffer(make([]byte, 0, snifferSize))

	// check request method
	switch req.Method {
	case http.MethodConnect:
		err = f.handleTunnel(reader, scanner)
	default:
		err = f.handleProxy(req.Method, req.Params, reader, scanner)
	}

	// handle error
	if err != nil {
		return fmt.Errorf("handle request failed: %w", err)
	}

	//forward process
	localAddr := make(chan string)
	route, err := router.GetRoute(req.Host)
	if err != nil {
		log.Printf("http: get route for [%s] error: %v", req.Host, err)
		if _, err = f.Conn.Write(MessageServiceUnavailable); err != nil {
			return fmt.Errorf("failed to send reply: %w", err)
		}
		return nil
	}

	// route found
	defer utils.Close(route)
	log.Printf("http: %s -> %s", net.JoinHostPort(req.Host, strconv.Itoa(int(req.Port))), route)

	// pipe for sniffer write-back
	r, w := io.Pipe()
	defer utils.CloseAll(w, r)

	// channel for receive err and wait for
	request := transport.NewRequest(req.Host, req.Port)
	proxyErr := make(chan error)

	// actual proxy
	go func() {
		proxyErr <- route.Proxy(context.Background(), request, localAddr, f.Conn, r)
	}()

	// internal process
	internalErr := make(chan error)
	go func() {
		// buffer rewrite -> reconstructed tcp raw msg
		if b := f.b.Bytes(); len(b) > 0 {
			if _, err := w.Write(b); err != nil {
				internalErr <- fmt.Errorf("write buffer err: %w", err)
				return
			}
		}

		//log.Println("src -> target start")
		// todo: use our own io copy function with custom buffer and error returning
		if _, err = io.CopyBuffer(w, f.Conn, make([]byte, transport.BufferSize)); err != nil {
			internalErr <- fmt.Errorf("%s: %w", "copy stream error", err)
		}
		//log.Println("src -> target done")
		close(internalErr)
	}()

	//log.Println("wait for local addr")
	//ld := <-localAddr
	//log.Printf("local addr: %s", ld)
	var b bytes.Buffer
	if addr, ok := <-localAddr; !ok || addr == "" {
		b.Write(MessageServiceUnavailable)
	} else if req.Method == http.MethodConnect {
		b.Write(MessageConnectionEstablished)
	}
	// message end
	b.WriteString(CRLF)

	if _, err = f.Conn.Write(b.Bytes()); err != nil {
		return fmt.Errorf("send http status error: %w", err)
	}

	select {
	case err = <-proxyErr:
		// notify proxy session is ended
		// todo: rpc only check server and client stream copy error
	case err1, ok := <-internalErr:
		if ok {
			err = err1
		}
	}
	return err
}
