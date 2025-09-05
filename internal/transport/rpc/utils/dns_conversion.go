package utils

import (
	"fmt"
	"net"

	proto "github.com/SuzukiHonoka/spaceship/v2/internal/transport/rpc/proto"
	"github.com/miekg/dns"
)

// ConvertRRToProto converts a dns.RR to a protobuf RR_Record using wire format serialization.
// This approach preserves all DNS record data and handles all DNS record types automatically.
func ConvertRRToProto(rr dns.RR) (*proto.RR_Record, error) {
	if rr == nil {
		return nil, fmt.Errorf("nil RR provided")
	}

	// Calculate required buffer size
	bufSize := dns.Len(rr)
	buf := make([]byte, bufSize)

	// Serialize RR to wire format
	off, err := dns.PackRR(rr, buf, 0, nil, false)
	if err != nil {
		return nil, fmt.Errorf("failed to pack RR: %w", err)
	}

	// Get header information for metadata
	header := rr.Header()
	return &proto.RR_Record{
		WireData: buf[:off],
		Name:     header.Name,
		Rrtype:   uint32(header.Rrtype),
		Class:    uint32(header.Class),
		Ttl:      header.Ttl,
	}, nil
}

// ConvertProtoToRR converts a protobuf RR_Record back to a dns.RR using wire format deserialization.
func ConvertProtoToRR(protoRR *proto.RR_Record) (dns.RR, error) {
	if protoRR == nil {
		return nil, fmt.Errorf("nil proto RR provided")
	}

	if len(protoRR.WireData) == 0 {
		return nil, fmt.Errorf("empty wire data")
	}

	// Deserialize from wire format
	rr, _, err := dns.UnpackRR(protoRR.WireData, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to unpack RR: %w", err)
	}

	return rr, nil
}

// ConvertRRSliceToProto converts a slice of dns.RR to protobuf RR_Records.
func ConvertRRSliceToProto(rrs []dns.RR) ([]*proto.RR_Record, error) {
	if len(rrs) == 0 {
		return nil, nil
	}

	protoRRs := make([]*proto.RR_Record, 0, len(rrs))
	for _, rr := range rrs {
		protoRR, err := ConvertRRToProto(rr)
		if err != nil {
			// Log error but continue with other records
			continue
		}
		protoRRs = append(protoRRs, protoRR)
	}

	return protoRRs, nil
}

// ConvertProtoToRRSlice converts protobuf RR_Records back to dns.RR slice.
func ConvertProtoToRRSlice(protoRRs []*proto.RR_Record) ([]dns.RR, error) {
	if len(protoRRs) == 0 {
		return nil, nil
	}

	rrs := make([]dns.RR, 0, len(protoRRs))
	for _, protoRR := range protoRRs {
		rr, err := ConvertProtoToRR(protoRR)
		if err != nil {
			// Log error but continue with other records
			continue
		}
		rrs = append(rrs, rr)
	}

	return rrs, nil
}

// CreateBasicRRFromTypeAndIP creates basic A/AAAA records for simple IP resolution
// This is a helper function for basic IP-only responses
func CreateBasicRRFromTypeAndIP(name string, qtype uint16, ip net.IP, ttl uint32) dns.RR {
	header := dns.RR_Header{
		Name:   dns.Fqdn(name),
		Rrtype: qtype,
		Class:  dns.ClassINET,
		Ttl:    ttl,
	}

	switch qtype {
	case dns.TypeA:
		if ipv4 := ip.To4(); ipv4 != nil {
			return &dns.A{
				Hdr: header,
				A:   ipv4,
			}
		}
	case dns.TypeAAAA:
		if ipv6 := ip.To16(); ipv6 != nil && ip.To4() == nil {
			return &dns.AAAA{
				Hdr:  header,
				AAAA: ipv6,
			}
		}
	}

	return nil
}

// GetRRTypeString returns human-readable DNS record type string
func GetRRTypeString(rrtype uint16) string {
	if typeStr, ok := dns.TypeToString[rrtype]; ok {
		return typeStr
	}
	return fmt.Sprintf("TYPE%d", rrtype)
}

// GetRRClassString returns human-readable DNS class string
func GetRRClassString(class uint16) string {
	if classStr, ok := dns.ClassToString[class]; ok {
		return classStr
	}
	return fmt.Sprintf("CLASS%d", class)
}
