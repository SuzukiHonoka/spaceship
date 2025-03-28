package http

import (
	"net/http"
)

type Filter []string

// The following hopHeaders are copied from https://golang.org/src/net/http/httputil/reverseproxy.go
// Hop-by-hop headers. These are removed when sent to the backend.
// As of RFC 7230, hop-by-hop headers are required to appear in the
// Connection header field. These are the headers defined by the
// obsoleted RFC 2616 (section 13.5.1) and are used for backward
// compatibility.
var hopHeaders = Filter{
	"Connection",
	"Proxy-Connection", // non-standard but still sent by libcurl and rejected by e.g. google
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",      // canonicalized version of "TE"
	"Trailer", // not Trailers per URL above; https://www.rfc-editor.org/errata_search.php?eid=4522
	"Transfer-Encoding",
	"Upgrade",
}

// RemoveHopHeaders removes hop-by-hop headers from http.Header
// note that only the request which not CONNECT one needs to do this
func (f Filter) RemoveHopHeaders(h http.Header) {
	for _, k := range f {
		h.Del(k)
	}
}
