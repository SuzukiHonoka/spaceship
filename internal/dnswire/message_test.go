package dnswire

import (
	"errors"
	"testing"

	"github.com/miekg/dns"
)

func packedQuery(t *testing.T, name string, qtype uint16) []byte {
	t.Helper()
	msg := new(dns.Msg)
	msg.SetQuestion(name, qtype)
	wire, err := msg.Pack()
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestParseQuery(t *testing.T) {
	valid := packedQuery(t, "example.com.", dns.TypeA)
	msg, err := ParseQuery(valid)
	if err != nil {
		t.Fatalf("ParseQuery(valid) error = %v", err)
	}
	if len(msg.Question) != 1 || msg.Question[0].Name != "example.com." {
		t.Fatalf("ParseQuery(valid) question = %+v", msg.Question)
	}

	tests := []struct {
		name         string
		wire         []byte
		want         error
		wantAnyError bool
	}{
		{name: "empty", want: ErrEmptyQuery},
		{name: "malformed", wire: []byte{0x01}, wantAnyError: true},
		{
			name: "oversized",
			wire: make([]byte, MaxMessageSize+1),
			want: ErrMessageTooLarge,
		},
		{
			name: "response",
			wire: func() []byte {
				m := new(dns.Msg)
				m.SetReply(msg)
				wire, _ := m.Pack()
				return wire
			}(),
			want: ErrNotQuery,
		},
		{
			name: "unsupported opcode",
			wire: func() []byte {
				m := new(dns.Msg)
				m.SetQuestion("example.com.", dns.TypeA)
				m.Opcode = dns.OpcodeUpdate
				wire, _ := m.Pack()
				return wire
			}(),
			want: ErrUnsupportedOpcode,
		},
		{
			name: "no question",
			wire: func() []byte {
				m := new(dns.Msg)
				wire, _ := m.Pack()
				return wire
			}(),
			want: ErrQuestionCount,
		},
		{
			name: "non IN class",
			wire: func() []byte {
				m := new(dns.Msg)
				m.Question = []dns.Question{{
					Name:   "example.com.",
					Qtype:  dns.TypeA,
					Qclass: dns.ClassCHAOS,
				}}
				wire, _ := m.Pack()
				return wire
			}(),
			want: ErrUnsupportedClass,
		},
		{
			name: "zone transfer",
			wire: packedQuery(t, "example.com.", dns.TypeAXFR),
			want: ErrZoneTransfer,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseQuery(tt.wire)
			if tt.wantAnyError {
				if err == nil {
					t.Fatal("ParseQuery(malformed) error = nil")
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("ParseQuery() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestErrorResponseAndEmptySuccess(t *testing.T) {
	query := new(dns.Msg)
	query.SetQuestion("example.com.", dns.TypeAAAA)
	query.Id = 0x1234
	query.RecursionDesired = true
	query.SetEdns0(1232, true)

	response := ErrorResponse(query, dns.RcodeServerFailure)
	if !response.Response || response.Id != query.Id ||
		response.Rcode != dns.RcodeServerFailure ||
		!response.RecursionAvailable ||
		!QuestionsEqual(query, response) {
		t.Fatalf("ErrorResponse() = %+v", response)
	}
	if opt := response.IsEdns0(); opt == nil || opt.UDPSize() != 1232 || !opt.Do() {
		t.Fatalf("ErrorResponse() EDNS = %+v", opt)
	}

	empty := EmptySuccessResponse(query)
	if empty.Rcode != dns.RcodeSuccess || len(empty.Answer) != 0 {
		t.Fatalf("EmptySuccessResponse() = %+v", empty)
	}

	withoutQuery := ErrorResponse(nil, dns.RcodeFormatError)
	if !withoutQuery.Response || withoutQuery.Rcode != dns.RcodeFormatError ||
		len(withoutQuery.Question) != 0 {
		t.Fatalf("ErrorResponse(nil) = %+v", withoutQuery)
	}
}

func TestQueryErrorRcode(t *testing.T) {
	if got := QueryErrorRcode(ErrUnsupportedOpcode); got != dns.RcodeNotImplemented {
		t.Fatalf("unsupported opcode rcode = %d, want NOTIMP", got)
	}
	if got := QueryErrorRcode(ErrZoneTransfer); got != dns.RcodeRefused {
		t.Fatalf("zone transfer rcode = %d, want REFUSED", got)
	}
	if got := QueryErrorRcode(ErrQuestionCount); got != dns.RcodeFormatError {
		t.Fatalf("malformed query rcode = %d, want FORMERR", got)
	}
}

func TestQuestionsEqualAndUDPSize(t *testing.T) {
	query := new(dns.Msg)
	query.SetQuestion("Example.COM.", dns.TypeA)
	query.Id = 7
	response := new(dns.Msg)
	response.SetReply(query)
	response.Question[0].Name = "example.com."
	if !QuestionsEqual(query, response) {
		t.Fatal("QuestionsEqual() rejected case-insensitive name")
	}
	response.Id++
	if QuestionsEqual(query, response) {
		t.Fatal("QuestionsEqual() accepted mismatched ID")
	}
	response.Id = query.Id
	response.Opcode = dns.OpcodeUpdate
	if QuestionsEqual(query, response) {
		t.Fatal("QuestionsEqual() accepted mismatched opcode")
	}
	response.Opcode = query.Opcode

	if got := UDPSize(query); got != dns.MinMsgSize {
		t.Fatalf("UDPSize(no EDNS) = %d, want %d", got, dns.MinMsgSize)
	}
	query.SetEdns0(4096, false)
	if got := UDPSize(query); got != 4096 {
		t.Fatalf("UDPSize(EDNS) = %d, want 4096", got)
	}
	maxQuery := new(dns.Msg)
	maxQuery.SetQuestion("example.com.", dns.TypeA)
	maxQuery.SetEdns0(65535, false)
	if got := UDPSize(maxQuery); got != MaxUDPMessageSize {
		t.Fatalf("UDPSize(max EDNS) = %d, want %d", got, MaxUDPMessageSize)
	}

	if QuestionsEqual(nil, response) || QuestionsEqual(query, nil) {
		t.Fatal("QuestionsEqual accepted a nil message")
	}
	response.SetReply(query)
	response.Question = append(response.Question, dns.Question{
		Name:   "extra.example.",
		Qtype:  dns.TypeA,
		Qclass: dns.ClassINET,
	})
	if QuestionsEqual(query, response) {
		t.Fatal("QuestionsEqual accepted a different question count")
	}
	response.SetReply(query)
	response.Question[0].Qtype = dns.TypeAAAA
	if QuestionsEqual(query, response) {
		t.Fatal("QuestionsEqual accepted a different question type")
	}
	response.SetReply(query)
	response.Question[0].Qclass = dns.ClassCHAOS
	if QuestionsEqual(query, response) {
		t.Fatal("QuestionsEqual accepted a different question class")
	}
}
