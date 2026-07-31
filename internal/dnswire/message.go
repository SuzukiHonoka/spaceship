package dnswire

import (
	"errors"
	"fmt"
	"strings"

	"github.com/miekg/dns"
)

const (
	MaxMessageSize    = dns.MaxMsgSize
	MaxUDPMessageSize = 65507
)

var (
	ErrEmptyQuery        = errors.New("dns: empty wire query")
	ErrMessageTooLarge   = errors.New("dns: wire message exceeds 65535 bytes")
	ErrNotQuery          = errors.New("dns: message is not a query")
	ErrUnsupportedOpcode = errors.New("dns: unsupported opcode")
	ErrQuestionCount     = errors.New("dns: exactly one question is required")
	ErrUnsupportedClass  = errors.New("dns: unsupported question class")
	ErrZoneTransfer      = errors.New("dns: zone transfers are not supported")
)

// ParseQuery validates and unpacks one ordinary recursive DNS query. TUN DNS
// hijacking is not an authoritative server and must not become a path for
// UPDATE, NOTIFY, or zone-transfer traffic.
func ParseQuery(wire []byte) (*dns.Msg, error) {
	if len(wire) == 0 {
		return nil, ErrEmptyQuery
	}
	if len(wire) > MaxMessageSize {
		return nil, ErrMessageTooLarge
	}

	msg := new(dns.Msg)
	if err := msg.Unpack(wire); err != nil {
		return nil, fmt.Errorf("dns: unpack query: %w", err)
	}
	if msg.Response {
		return nil, ErrNotQuery
	}
	if msg.Opcode != dns.OpcodeQuery {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedOpcode, msg.Opcode)
	}
	if len(msg.Question) != 1 {
		return nil, fmt.Errorf("%w: %d", ErrQuestionCount, len(msg.Question))
	}

	question := msg.Question[0]
	if question.Qclass != dns.ClassINET {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedClass, question.Qclass)
	}
	switch question.Qtype {
	case dns.TypeAXFR, dns.TypeIXFR:
		return nil, fmt.Errorf("%w: %s", ErrZoneTransfer, dns.TypeToString[question.Qtype])
	}

	return msg, nil
}

// QueryErrorRcode maps query-validation failures to protocol-level responses.
// Unsupported operations are distinguished from malformed wire data so every
// DNS frontend returns the same result without contacting an upstream.
func QueryErrorRcode(err error) int {
	switch {
	case errors.Is(err, ErrUnsupportedOpcode):
		return dns.RcodeNotImplemented
	case errors.Is(err, ErrZoneTransfer):
		return dns.RcodeRefused
	default:
		return dns.RcodeFormatError
	}
}

// ErrorResponse creates a protocol-level error that retains the original
// transaction ID and question. Callers use this instead of falling back to a
// local resolver when the Spaceship server cannot answer.
func ErrorResponse(query *dns.Msg, rcode int) *dns.Msg {
	response := new(dns.Msg)
	if query == nil {
		response.MsgHdr.Response = true
		response.Rcode = rcode
		return response
	}
	response.SetReply(query)
	response.RecursionAvailable = true
	response.Rcode = rcode
	if opt := query.IsEdns0(); opt != nil {
		response.SetEdns0(opt.UDPSize(), opt.Do())
	}
	return response
}

// EmptySuccessResponse returns NOERROR/NODATA for a policy-blocked question.
func EmptySuccessResponse(query *dns.Msg) *dns.Msg {
	return ErrorResponse(query, dns.RcodeSuccess)
}

// QuestionsEqual verifies that an upstream response corresponds to the query,
// in addition to the transaction-ID check performed by miekg/dns.
func QuestionsEqual(query, response *dns.Msg) bool {
	if query == nil || response == nil ||
		query.Id != response.Id ||
		query.Opcode != response.Opcode ||
		len(query.Question) != len(response.Question) {
		return false
	}
	for i := range query.Question {
		want := query.Question[i]
		got := response.Question[i]
		if !strings.EqualFold(want.Name, got.Name) ||
			want.Qtype != got.Qtype ||
			want.Qclass != got.Qclass {
			return false
		}
	}
	return true
}

// UDPSize returns the maximum response size advertised by a query.
func UDPSize(query *dns.Msg) int {
	if query != nil {
		if opt := query.IsEdns0(); opt != nil {
			if size := int(opt.UDPSize()); size >= dns.MinMsgSize {
				return min(size, MaxUDPMessageSize)
			}
		}
	}
	return dns.MinMsgSize
}
