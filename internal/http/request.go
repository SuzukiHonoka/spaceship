package http

import (
	"errors"
	"fmt"
	"github.com/SuzukiHonoka/spaceship/internal/transport"
	"github.com/SuzukiHonoka/spaceship/internal/utils"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type Request struct {
	Method string
	Host   string
	Params string
	Port   uint16
}

func ParseRawParamsFromUrl(scheme bool, url string) (string, error) {
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

// ParseRequestFromRaw parses request from raw tcp message
func ParseRequestFromRaw(raw string) (*Request, error) {
	method, rest, ok1 := strings.Cut(raw, " ")
	targetRawUri, _, ok2 := strings.Cut(rest, " ")
	// we are not a http website
	if targetRawUri == "/" {
		return nil, transport.ErrBadRequest
	}
	// proper request format at first line: (HTTP_METHOD TARGET_URL HTTP_VERSION)
	// -> GET https://www.google.com HTTP/1.1
	// it should have 3 elements divided by space
	if !ok1 || !ok2 {
		return nil, transport.ErrBadRequest
	}
	//log.Println(method, targetRawUri)
	var r Request
	r.Method = method
	switch method {
	case http.MethodConnect:
		// no scheme
		// CONNECT www.google.com:443 HTTP/1.1
		var err error
		if r.Host, r.Port, err = utils.SplitHostPort(targetRawUri); err != nil {
			return nil, err
		}
	default:
		// parse URL from raw
		targetUrl, err := url.Parse(targetRawUri)
		// if not a legal url format
		if err != nil {
			return nil, err
		}
		// mark
		hasScheme := targetUrl.Scheme != ""
		// divide the host and port
		// this will raise error if port not found
		// 1. http://google.com 2. google.com
		if r.Host, r.Port, err = utils.SplitHostPort(targetUrl.Host); err != nil {
			// other error
			var addrErr *net.AddrError
			errors.As(err, &addrErr)
			if addrErr.Err != "missing port in address" {
				return nil, err
			}
			r.Host = targetUrl.Host
			if hasScheme {
				if v, ok := ProtocolMap[targetUrl.Scheme]; ok {
					r.Port = v
				} else {
					err = fmt.Errorf("unkown scheme: %s %w", targetUrl.Scheme, transport.ErrBadRequest)
					return nil, err
				}
			} else {
				r.Port = 80
			}
		}
		if r.Params, err = ParseRawParamsFromUrl(hasScheme, targetRawUri); err != nil {
			return nil, err
		}
	}
	//log.Printf("request parsed: %+v\n", r)
	return &r, nil
}

func (r *Request) UnpackIPv6() {
	if r.Host[0] == '[' && r.Host[len(r.Host)-1] == ']' {
		r.Host = r.Host[1 : len(r.Host)-1]
	}
}
