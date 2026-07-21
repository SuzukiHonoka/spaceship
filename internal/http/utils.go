package http

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
)

// ServeError logs err and writes a 503 response when the client still expects
// HTTP framing. Prefer ServeProxyError when a target host is known.
func ServeError(w io.Writer, err error) {
	ServeProxyError(w, "", err)
}

// ServeProxyError logs a single readable failure line and writes 503 when
// appropriate.
//
//	http: proxy example.com:443 failed: server ack timeout
//
// Benign terminal conditions (nil, EOF, context canceled) are silent and do
// not write a response body — the peer is already gone or the session was
// deliberately aborted.
func ServeProxyError(w io.Writer, host string, err error) {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return
	}

	if host != "" {
		log.Printf("http: proxy %s failed: %v", host, err)
	} else {
		log.Printf("http: %v", err)
	}

	if w == nil {
		return
	}

	if rw, ok := w.(http.ResponseWriter); ok {
		http.Error(rw, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}

	if _, writeErr := w.Write(MessageServiceUnavailable); writeErr != nil {
		log.Printf("http: write error status failed: %v", writeErr)
	}
}

func BuildRemoteAddr(r *http.Request) (string, string, error) {
	host, port, err := utils.SplitHostPort(r.Host)
	if err != nil {
		// check if a standard port missing, eg: http
		if addrErr, ok := errors.AsType[*net.AddrError](err); !ok || addrErr.Err != "missing port in address" {
			return "", "", fmt.Errorf("http: split host port error: %w", err)
		}

		// missing port in address
		host = r.Host
		if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
			host = host[1 : len(host)-1]
		}
		if r.URL.Scheme != "" {
			var ok bool
			if port, ok = ProtocolPortMap[r.URL.Scheme]; !ok {
				return "", "", fmt.Errorf("unkown scheme: %s %w", r.URL.Scheme, transport.ErrBadRequest)
			}
		} else {
			port = 80
		}
	}
	return host, net.JoinHostPort(host, strconv.FormatUint(uint64(port), 10)), nil
}
