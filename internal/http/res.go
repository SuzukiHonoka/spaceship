package http

// CRLF used for delimiter purpose, may be incompatible in some old programs
var CRLF = "\r\n"

var ProtocolMap = map[string]uint16{
	"http":  80,
	"https": 443,
}

var (
	MessageConnectionEstablished = []byte("HTTP/1.1 200 Connection established" + CRLF)
	MessageServiceUnavailable    = []byte("HTTP/1.1 503 Service Unavailable" + CRLF + CRLF)
)
