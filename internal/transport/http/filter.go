package http

type Filter []string

// refer http://www.w3.org/Protocols/rfc2616/rfc2616-sec13.html
var hopHeaders = Filter{
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

// Filter checks if given header needed to be filtered
// note that only the request which not CONNECT one needs to do this
func (f Filter) Filter(s string) bool {
	for _, v := range f {
		if v == s {
			return true
		}
	}
	return false
}
