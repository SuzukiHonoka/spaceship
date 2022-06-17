package http

import "strings"

type Filter map[string]struct{}

var hopHeadersMap = make(Filter)

func init() {
	// refer http://www.w3.org/Protocols/rfc2616/rfc2616-sec13.html
	hopHeaders := []string{
		"connection",
		"keep-alive",
		"proxy-connection",
		"proxy-authenticate",
		"proxy-authorization",
		"te",
		"trailers",
		"transfer-encoding",
		"upgrade",
	}
	for _, header := range hopHeaders {
		hopHeadersMap[header] = struct{}{}
	}
}

// Filter checks if given header needed to be filtered
// note that only the request which not CONNECT one needs to do this
func (f Filter) Filter(s string) bool {
	_, ok := f[strings.ToLower(s)]
	return ok
}
