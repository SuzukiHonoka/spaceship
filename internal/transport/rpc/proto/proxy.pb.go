// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.6
// 	protoc        v5.29.3
// source: proxy.proto

package proxy

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
	unsafe "unsafe"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type ProxyStatus int32

const (
	ProxyStatus_Session  ProxyStatus = 0
	ProxyStatus_Error    ProxyStatus = 1
	ProxyStatus_Accepted ProxyStatus = 2
	ProxyStatus_EOF      ProxyStatus = 3
)

// Enum value maps for ProxyStatus.
var (
	ProxyStatus_name = map[int32]string{
		0: "Session",
		1: "Error",
		2: "Accepted",
		3: "EOF",
	}
	ProxyStatus_value = map[string]int32{
		"Session":  0,
		"Error":    1,
		"Accepted": 2,
		"EOF":      3,
	}
)

func (x ProxyStatus) Enum() *ProxyStatus {
	p := new(ProxyStatus)
	*p = x
	return p
}

func (x ProxyStatus) String() string {
	return protoimpl.X.EnumStringOf(x.Descriptor(), protoreflect.EnumNumber(x))
}

func (ProxyStatus) Descriptor() protoreflect.EnumDescriptor {
	return file_proxy_proto_enumTypes[0].Descriptor()
}

func (ProxyStatus) Type() protoreflect.EnumType {
	return &file_proxy_proto_enumTypes[0]
}

func (x ProxyStatus) Number() protoreflect.EnumNumber {
	return protoreflect.EnumNumber(x)
}

// Deprecated: Use ProxyStatus.Descriptor instead.
func (ProxyStatus) EnumDescriptor() ([]byte, []int) {
	return file_proxy_proto_rawDescGZIP(), []int{0}
}

type ProxySRC struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// Types that are valid to be assigned to HeaderOrPayload:
	//
	//	*ProxySRC_Header
	//	*ProxySRC_Payload
	HeaderOrPayload isProxySRC_HeaderOrPayload `protobuf_oneof:"header_or_payload"`
	unknownFields   protoimpl.UnknownFields
	sizeCache       protoimpl.SizeCache
}

func (x *ProxySRC) Reset() {
	*x = ProxySRC{}
	mi := &file_proxy_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ProxySRC) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ProxySRC) ProtoMessage() {}

func (x *ProxySRC) ProtoReflect() protoreflect.Message {
	mi := &file_proxy_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ProxySRC.ProtoReflect.Descriptor instead.
func (*ProxySRC) Descriptor() ([]byte, []int) {
	return file_proxy_proto_rawDescGZIP(), []int{0}
}

func (x *ProxySRC) GetHeaderOrPayload() isProxySRC_HeaderOrPayload {
	if x != nil {
		return x.HeaderOrPayload
	}
	return nil
}

func (x *ProxySRC) GetHeader() *ProxySRC_ProxyHeader {
	if x != nil {
		if x, ok := x.HeaderOrPayload.(*ProxySRC_Header); ok {
			return x.Header
		}
	}
	return nil
}

func (x *ProxySRC) GetPayload() []byte {
	if x != nil {
		if x, ok := x.HeaderOrPayload.(*ProxySRC_Payload); ok {
			return x.Payload
		}
	}
	return nil
}

type isProxySRC_HeaderOrPayload interface {
	isProxySRC_HeaderOrPayload()
}

type ProxySRC_Header struct {
	Header *ProxySRC_ProxyHeader `protobuf:"bytes,1,opt,name=header,proto3,oneof"`
}

type ProxySRC_Payload struct {
	Payload []byte `protobuf:"bytes,2,opt,name=payload,proto3,oneof"`
}

func (*ProxySRC_Header) isProxySRC_HeaderOrPayload() {}

func (*ProxySRC_Payload) isProxySRC_HeaderOrPayload() {}

type ProxyDST struct {
	state  protoimpl.MessageState `protogen:"open.v1"`
	Status ProxyStatus            `protobuf:"varint,1,opt,name=status,proto3,enum=proxy.ProxyStatus" json:"status,omitempty"`
	// Types that are valid to be assigned to HeaderOrPayload:
	//
	//	*ProxyDST_Header
	//	*ProxyDST_Payload
	HeaderOrPayload isProxyDST_HeaderOrPayload `protobuf_oneof:"header_or_payload"`
	unknownFields   protoimpl.UnknownFields
	sizeCache       protoimpl.SizeCache
}

func (x *ProxyDST) Reset() {
	*x = ProxyDST{}
	mi := &file_proxy_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ProxyDST) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ProxyDST) ProtoMessage() {}

func (x *ProxyDST) ProtoReflect() protoreflect.Message {
	mi := &file_proxy_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ProxyDST.ProtoReflect.Descriptor instead.
func (*ProxyDST) Descriptor() ([]byte, []int) {
	return file_proxy_proto_rawDescGZIP(), []int{1}
}

func (x *ProxyDST) GetStatus() ProxyStatus {
	if x != nil {
		return x.Status
	}
	return ProxyStatus_Session
}

func (x *ProxyDST) GetHeaderOrPayload() isProxyDST_HeaderOrPayload {
	if x != nil {
		return x.HeaderOrPayload
	}
	return nil
}

func (x *ProxyDST) GetHeader() *ProxyDST_ProxyHeader {
	if x != nil {
		if x, ok := x.HeaderOrPayload.(*ProxyDST_Header); ok {
			return x.Header
		}
	}
	return nil
}

func (x *ProxyDST) GetPayload() []byte {
	if x != nil {
		if x, ok := x.HeaderOrPayload.(*ProxyDST_Payload); ok {
			return x.Payload
		}
	}
	return nil
}

type isProxyDST_HeaderOrPayload interface {
	isProxyDST_HeaderOrPayload()
}

type ProxyDST_Header struct {
	Header *ProxyDST_ProxyHeader `protobuf:"bytes,2,opt,name=header,proto3,oneof"`
}

type ProxyDST_Payload struct {
	Payload []byte `protobuf:"bytes,3,opt,name=payload,proto3,oneof"`
}

func (*ProxyDST_Header) isProxyDST_HeaderOrPayload() {}

func (*ProxyDST_Payload) isProxyDST_HeaderOrPayload() {}

type DnsRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// user id
	Id string `protobuf:"bytes,1,opt,name=id,proto3" json:"id,omitempty"`
	// fqdn name
	Fqdn string `protobuf:"bytes,2,opt,name=fqdn,proto3" json:"fqdn,omitempty"`
	// enable ipv6
	Ipv6          bool `protobuf:"varint,3,opt,name=ipv6,proto3" json:"ipv6,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *DnsRequest) Reset() {
	*x = DnsRequest{}
	mi := &file_proxy_proto_msgTypes[2]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *DnsRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*DnsRequest) ProtoMessage() {}

func (x *DnsRequest) ProtoReflect() protoreflect.Message {
	mi := &file_proxy_proto_msgTypes[2]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use DnsRequest.ProtoReflect.Descriptor instead.
func (*DnsRequest) Descriptor() ([]byte, []int) {
	return file_proxy_proto_rawDescGZIP(), []int{2}
}

func (x *DnsRequest) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *DnsRequest) GetFqdn() string {
	if x != nil {
		return x.Fqdn
	}
	return ""
}

func (x *DnsRequest) GetIpv6() bool {
	if x != nil {
		return x.Ipv6
	}
	return false
}

type DnsResponse struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// fqdn name
	Domain string `protobuf:"bytes,1,opt,name=domain,proto3" json:"domain,omitempty"`
	// fqdn resolved result
	Result        []string `protobuf:"bytes,2,rep,name=result,proto3" json:"result,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *DnsResponse) Reset() {
	*x = DnsResponse{}
	mi := &file_proxy_proto_msgTypes[3]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *DnsResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*DnsResponse) ProtoMessage() {}

func (x *DnsResponse) ProtoReflect() protoreflect.Message {
	mi := &file_proxy_proto_msgTypes[3]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use DnsResponse.ProtoReflect.Descriptor instead.
func (*DnsResponse) Descriptor() ([]byte, []int) {
	return file_proxy_proto_rawDescGZIP(), []int{3}
}

func (x *DnsResponse) GetDomain() string {
	if x != nil {
		return x.Domain
	}
	return ""
}

func (x *DnsResponse) GetResult() []string {
	if x != nil {
		return x.Result
	}
	return nil
}

type ProxySRC_ProxyHeader struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// user id
	Id string `protobuf:"bytes,1,opt,name=id,proto3" json:"id,omitempty"`
	// fqdn
	Fqdn string `protobuf:"bytes,2,opt,name=fqdn,proto3" json:"fqdn,omitempty"`
	// port
	Port          uint32 `protobuf:"varint,3,opt,name=port,proto3" json:"port,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ProxySRC_ProxyHeader) Reset() {
	*x = ProxySRC_ProxyHeader{}
	mi := &file_proxy_proto_msgTypes[4]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ProxySRC_ProxyHeader) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ProxySRC_ProxyHeader) ProtoMessage() {}

func (x *ProxySRC_ProxyHeader) ProtoReflect() protoreflect.Message {
	mi := &file_proxy_proto_msgTypes[4]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ProxySRC_ProxyHeader.ProtoReflect.Descriptor instead.
func (*ProxySRC_ProxyHeader) Descriptor() ([]byte, []int) {
	return file_proxy_proto_rawDescGZIP(), []int{0, 0}
}

func (x *ProxySRC_ProxyHeader) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *ProxySRC_ProxyHeader) GetFqdn() string {
	if x != nil {
		return x.Fqdn
	}
	return ""
}

func (x *ProxySRC_ProxyHeader) GetPort() uint32 {
	if x != nil {
		return x.Port
	}
	return 0
}

type ProxyDST_ProxyHeader struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Addr          string                 `protobuf:"bytes,2,opt,name=addr,proto3" json:"addr,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ProxyDST_ProxyHeader) Reset() {
	*x = ProxyDST_ProxyHeader{}
	mi := &file_proxy_proto_msgTypes[5]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ProxyDST_ProxyHeader) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ProxyDST_ProxyHeader) ProtoMessage() {}

func (x *ProxyDST_ProxyHeader) ProtoReflect() protoreflect.Message {
	mi := &file_proxy_proto_msgTypes[5]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ProxyDST_ProxyHeader.ProtoReflect.Descriptor instead.
func (*ProxyDST_ProxyHeader) Descriptor() ([]byte, []int) {
	return file_proxy_proto_rawDescGZIP(), []int{1, 0}
}

func (x *ProxyDST_ProxyHeader) GetAddr() string {
	if x != nil {
		return x.Addr
	}
	return ""
}

var File_proxy_proto protoreflect.FileDescriptor

const file_proxy_proto_rawDesc = "" +
	"\n" +
	"\vproxy.proto\x12\x05proxy\"\xb9\x01\n" +
	"\bProxySRC\x125\n" +
	"\x06header\x18\x01 \x01(\v2\x1b.proxy.ProxySRC.ProxyHeaderH\x00R\x06header\x12\x1a\n" +
	"\apayload\x18\x02 \x01(\fH\x00R\apayload\x1aE\n" +
	"\vProxyHeader\x12\x0e\n" +
	"\x02id\x18\x01 \x01(\tR\x02id\x12\x12\n" +
	"\x04fqdn\x18\x02 \x01(\tR\x04fqdn\x12\x12\n" +
	"\x04port\x18\x03 \x01(\rR\x04portB\x13\n" +
	"\x11header_or_payload\"\xc1\x01\n" +
	"\bProxyDST\x12*\n" +
	"\x06status\x18\x01 \x01(\x0e2\x12.proxy.ProxyStatusR\x06status\x125\n" +
	"\x06header\x18\x02 \x01(\v2\x1b.proxy.ProxyDST.ProxyHeaderH\x00R\x06header\x12\x1a\n" +
	"\apayload\x18\x03 \x01(\fH\x00R\apayload\x1a!\n" +
	"\vProxyHeader\x12\x12\n" +
	"\x04addr\x18\x02 \x01(\tR\x04addrB\x13\n" +
	"\x11header_or_payload\"D\n" +
	"\n" +
	"DnsRequest\x12\x0e\n" +
	"\x02id\x18\x01 \x01(\tR\x02id\x12\x12\n" +
	"\x04fqdn\x18\x02 \x01(\tR\x04fqdn\x12\x12\n" +
	"\x04ipv6\x18\x03 \x01(\bR\x04ipv6\"=\n" +
	"\vDnsResponse\x12\x16\n" +
	"\x06domain\x18\x01 \x01(\tR\x06domain\x12\x16\n" +
	"\x06result\x18\x02 \x03(\tR\x06result*<\n" +
	"\vProxyStatus\x12\v\n" +
	"\aSession\x10\x00\x12\t\n" +
	"\x05Error\x10\x01\x12\f\n" +
	"\bAccepted\x10\x02\x12\a\n" +
	"\x03EOF\x10\x032s\n" +
	"\x05Proxy\x129\n" +
	"\n" +
	"DnsResolve\x12\x11.proxy.DnsRequest\x1a\x12.proxy.DnsResponse\"\x00(\x010\x01\x12/\n" +
	"\x05Proxy\x12\x0f.proxy.ProxySRC\x1a\x0f.proxy.ProxyDST\"\x00(\x010\x01BFZDgithub.com/SuzukiHonoka/spaceship/internal/transport/rpc/proto;proxyb\x06proto3"

var (
	file_proxy_proto_rawDescOnce sync.Once
	file_proxy_proto_rawDescData []byte
)

func file_proxy_proto_rawDescGZIP() []byte {
	file_proxy_proto_rawDescOnce.Do(func() {
		file_proxy_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_proxy_proto_rawDesc), len(file_proxy_proto_rawDesc)))
	})
	return file_proxy_proto_rawDescData
}

var file_proxy_proto_enumTypes = make([]protoimpl.EnumInfo, 1)
var file_proxy_proto_msgTypes = make([]protoimpl.MessageInfo, 6)
var file_proxy_proto_goTypes = []any{
	(ProxyStatus)(0),             // 0: proxy.ProxyStatus
	(*ProxySRC)(nil),             // 1: proxy.ProxySRC
	(*ProxyDST)(nil),             // 2: proxy.ProxyDST
	(*DnsRequest)(nil),           // 3: proxy.DnsRequest
	(*DnsResponse)(nil),          // 4: proxy.DnsResponse
	(*ProxySRC_ProxyHeader)(nil), // 5: proxy.ProxySRC.ProxyHeader
	(*ProxyDST_ProxyHeader)(nil), // 6: proxy.ProxyDST.ProxyHeader
}
var file_proxy_proto_depIdxs = []int32{
	5, // 0: proxy.ProxySRC.header:type_name -> proxy.ProxySRC.ProxyHeader
	0, // 1: proxy.ProxyDST.status:type_name -> proxy.ProxyStatus
	6, // 2: proxy.ProxyDST.header:type_name -> proxy.ProxyDST.ProxyHeader
	3, // 3: proxy.Proxy.DnsResolve:input_type -> proxy.DnsRequest
	1, // 4: proxy.Proxy.Proxy:input_type -> proxy.ProxySRC
	4, // 5: proxy.Proxy.DnsResolve:output_type -> proxy.DnsResponse
	2, // 6: proxy.Proxy.Proxy:output_type -> proxy.ProxyDST
	5, // [5:7] is the sub-list for method output_type
	3, // [3:5] is the sub-list for method input_type
	3, // [3:3] is the sub-list for extension type_name
	3, // [3:3] is the sub-list for extension extendee
	0, // [0:3] is the sub-list for field type_name
}

func init() { file_proxy_proto_init() }
func file_proxy_proto_init() {
	if File_proxy_proto != nil {
		return
	}
	file_proxy_proto_msgTypes[0].OneofWrappers = []any{
		(*ProxySRC_Header)(nil),
		(*ProxySRC_Payload)(nil),
	}
	file_proxy_proto_msgTypes[1].OneofWrappers = []any{
		(*ProxyDST_Header)(nil),
		(*ProxyDST_Payload)(nil),
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_proxy_proto_rawDesc), len(file_proxy_proto_rawDesc)),
			NumEnums:      1,
			NumMessages:   6,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_proxy_proto_goTypes,
		DependencyIndexes: file_proxy_proto_depIdxs,
		EnumInfos:         file_proxy_proto_enumTypes,
		MessageInfos:      file_proxy_proto_msgTypes,
	}.Build()
	File_proxy_proto = out.File
	file_proxy_proto_goTypes = nil
	file_proxy_proto_depIdxs = nil
}
