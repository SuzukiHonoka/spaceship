package http

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"

	"github.com/SuzukiHonoka/spaceship/v2/internal/transport"
	"github.com/SuzukiHonoka/spaceship/v2/internal/utils"
)

func ServeError(w io.Writer, err error) {
	// errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ECONNRESET)
	if err == nil || errors.Is(err, io.EOF) {
		return
	}

	log.Println(err)
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

func BuildRemoteAddr(r *http.Request) (string, string, error) {
	host, port, err := utils.SplitHostPort(r.Host)
	if err != nil {
		// check if a standard port missing, eg: http
		var addrErr *net.AddrError
		if !errors.As(err, &addrErr) || addrErr.Err != "missing port in address" {
			return "", "", fmt.Errorf("http: split host port error: %w", err)
		}

		// missing port in address
		host = r.Host
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
