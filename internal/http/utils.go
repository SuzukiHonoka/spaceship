package http

import (
	"errors"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	"io"
	"log"
	"net"
	"net/http"
	"syscall"
)

func ServeError(w io.Writer, err error) {
	if err == nil || err == io.EOF || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET) {
		return
	}

	log.Printf("serve error: %v", err)
	if w == nil {
		return
	}
	_, err1 := w.Write(MessageServiceUnavailable)
	if err1 != nil {
		log.Printf("http: write error status failed: %v", err1)
		return
	}
	if _, err1 = fmt.Fprint(w, err); err1 != nil {
		log.Printf("http: write error message failed: %v", err1)
	}
}

func BuildRemoteRequest(r *http.Request) (*transport.Request, error) {
	host, port, err := utils.SplitHostPort(r.Host)
	if err != nil {
		// check if a standard port missing, eg: http
		var addrErr *net.AddrError
		if !errors.As(err, &addrErr) && addrErr.Err != "missing port in address" {
			return nil, fmt.Errorf("http: split host port error: %w", err)
		}

		// missing port in address
		host = r.Host
		if r.URL.Scheme != "" {
			var ok bool
			if port, ok = ProtocolPortMap[r.URL.Scheme]; !ok {
				return nil, fmt.Errorf("unkown scheme: %s %w", r.URL.Scheme, transport.ErrBadRequest)
			}
		} else {
			port = 80
		}
	}
	return transport.NewRequest(host, port), nil
}
