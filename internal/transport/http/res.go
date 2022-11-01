package http

// CRLF used for delimiter purpose, may be incompatible in some old programs
var CRLF = "\r\n"

var ProtocolMap = map[string]uint16{
	"http":  80,
	"https": 443,
}
