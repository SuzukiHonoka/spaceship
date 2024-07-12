// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.34.2
// 	protoc        v5.27.2
// source: proxy.proto

package proxy

import (
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	reflect "reflect"
	sync "sync"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

type ProxyStatus int32

const (
	ProxyStatus_Error    ProxyStatus = 0
	ProxyStatus_Accepted ProxyStatus = 1
	ProxyStatus_Session  ProxyStatus = 3
	ProxyStatus_EOF      ProxyStatus = 4
)

// Enum value maps for ProxyStatus.
var (
	ProxyStatus_name = map[int32]string{
		0: "Error",
		1: "Accepted",
		3: "Session",
		4: "EOF",
	}
	ProxyStatus_value = map[string]int32{
		"Error":    0,
		"Accepted": 1,
		"Session":  3,
		"EOF":      4,
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
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// user id
	Id string `protobuf:"bytes,1,opt,name=id,proto3" json:"id,omitempty"`
	// bytes
	Data []byte `protobuf:"bytes,2,opt,name=data,proto3" json:"data,omitempty"`
	// fqdn
	Fqdn string `protobuf:"bytes,3,opt,name=fqdn,proto3" json:"fqdn,omitempty"`
	// port
	Port uint32 `protobuf:"varint,4,opt,name=port,proto3" json:"port,omitempty"`
}

func (x *ProxySRC) Reset() {
	*x = ProxySRC{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proxy_proto_msgTypes[0]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *ProxySRC) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ProxySRC) ProtoMessage() {}

func (x *ProxySRC) ProtoReflect() protoreflect.Message {
	mi := &file_proxy_proto_msgTypes[0]
	if protoimpl.UnsafeEnabled && x != nil {
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

func (x *ProxySRC) GetId() string {
	if x != nil {
		return x.Id
	}
	return ""
}

func (x *ProxySRC) GetData() []byte {
	if x != nil {
		return x.Data
	}
	return nil
}

func (x *ProxySRC) GetFqdn() string {
	if x != nil {
		return x.Fqdn
	}
	return ""
}

func (x *ProxySRC) GetPort() uint32 {
	if x != nil {
		return x.Port
	}
	return 0
}

type ProxyDST struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// status
	Status ProxyStatus `protobuf:"varint,1,opt,name=status,proto3,enum=proxy.ProxyStatus" json:"status,omitempty"`
	// data bytes
	Data []byte `protobuf:"bytes,2,opt,name=data,proto3" json:"data,omitempty"` // EOF if failed
	// bind addr
	Addr string `protobuf:"bytes,3,opt,name=addr,proto3" json:"addr,omitempty"`
}

func (x *ProxyDST) Reset() {
	*x = ProxyDST{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proxy_proto_msgTypes[1]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *ProxyDST) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ProxyDST) ProtoMessage() {}

func (x *ProxyDST) ProtoReflect() protoreflect.Message {
	mi := &file_proxy_proto_msgTypes[1]
	if protoimpl.UnsafeEnabled && x != nil {
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
	return ProxyStatus_Error
}

func (x *ProxyDST) GetData() []byte {
	if x != nil {
		return x.Data
	}
	return nil
}

func (x *ProxyDST) GetAddr() string {
	if x != nil {
		return x.Addr
	}
	return ""
}

type DnsRequest struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// user uuid
	Uuid string `protobuf:"bytes,1,opt,name=uuid,proto3" json:"uuid,omitempty"`
	// fqdn name
	Fqdn string `protobuf:"bytes,2,opt,name=fqdn,proto3" json:"fqdn,omitempty"`
	// enable ipv6
	Ipv6 bool `protobuf:"varint,3,opt,name=ipv6,proto3" json:"ipv6,omitempty"`
}

func (x *DnsRequest) Reset() {
	*x = DnsRequest{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proxy_proto_msgTypes[2]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *DnsRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*DnsRequest) ProtoMessage() {}

func (x *DnsRequest) ProtoReflect() protoreflect.Message {
	mi := &file_proxy_proto_msgTypes[2]
	if protoimpl.UnsafeEnabled && x != nil {
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

func (x *DnsRequest) GetUuid() string {
	if x != nil {
		return x.Uuid
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
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	// fqdn name
	Domain string `protobuf:"bytes,1,opt,name=domain,proto3" json:"domain,omitempty"`
	// fqdn resolved result
	Result string `protobuf:"bytes,2,opt,name=result,proto3" json:"result,omitempty"`
}

func (x *DnsResponse) Reset() {
	*x = DnsResponse{}
	if protoimpl.UnsafeEnabled {
		mi := &file_proxy_proto_msgTypes[3]
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		ms.StoreMessageInfo(mi)
	}
}

func (x *DnsResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*DnsResponse) ProtoMessage() {}

func (x *DnsResponse) ProtoReflect() protoreflect.Message {
	mi := &file_proxy_proto_msgTypes[3]
	if protoimpl.UnsafeEnabled && x != nil {
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

func (x *DnsResponse) GetResult() string {
	if x != nil {
		return x.Result
	}
	return ""
}

var File_proxy_proto protoreflect.FileDescriptor

var file_proxy_proto_rawDesc = []byte{
	0x0a, 0x0b, 0x70, 0x72, 0x6f, 0x78, 0x79, 0x2e, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x12, 0x05, 0x70,
	0x72, 0x6f, 0x78, 0x79, 0x22, 0x56, 0x0a, 0x08, 0x50, 0x72, 0x6f, 0x78, 0x79, 0x53, 0x52, 0x43,
	0x12, 0x0e, 0x0a, 0x02, 0x69, 0x64, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x02, 0x69, 0x64,
	0x12, 0x12, 0x0a, 0x04, 0x64, 0x61, 0x74, 0x61, 0x18, 0x02, 0x20, 0x01, 0x28, 0x0c, 0x52, 0x04,
	0x64, 0x61, 0x74, 0x61, 0x12, 0x12, 0x0a, 0x04, 0x66, 0x71, 0x64, 0x6e, 0x18, 0x03, 0x20, 0x01,
	0x28, 0x09, 0x52, 0x04, 0x66, 0x71, 0x64, 0x6e, 0x12, 0x12, 0x0a, 0x04, 0x70, 0x6f, 0x72, 0x74,
	0x18, 0x04, 0x20, 0x01, 0x28, 0x0d, 0x52, 0x04, 0x70, 0x6f, 0x72, 0x74, 0x22, 0x5e, 0x0a, 0x08,
	0x50, 0x72, 0x6f, 0x78, 0x79, 0x44, 0x53, 0x54, 0x12, 0x2a, 0x0a, 0x06, 0x73, 0x74, 0x61, 0x74,
	0x75, 0x73, 0x18, 0x01, 0x20, 0x01, 0x28, 0x0e, 0x32, 0x12, 0x2e, 0x70, 0x72, 0x6f, 0x78, 0x79,
	0x2e, 0x50, 0x72, 0x6f, 0x78, 0x79, 0x53, 0x74, 0x61, 0x74, 0x75, 0x73, 0x52, 0x06, 0x73, 0x74,
	0x61, 0x74, 0x75, 0x73, 0x12, 0x12, 0x0a, 0x04, 0x64, 0x61, 0x74, 0x61, 0x18, 0x02, 0x20, 0x01,
	0x28, 0x0c, 0x52, 0x04, 0x64, 0x61, 0x74, 0x61, 0x12, 0x12, 0x0a, 0x04, 0x61, 0x64, 0x64, 0x72,
	0x18, 0x03, 0x20, 0x01, 0x28, 0x09, 0x52, 0x04, 0x61, 0x64, 0x64, 0x72, 0x22, 0x48, 0x0a, 0x0a,
	0x44, 0x6e, 0x73, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x12, 0x12, 0x0a, 0x04, 0x75, 0x75,
	0x69, 0x64, 0x18, 0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x04, 0x75, 0x75, 0x69, 0x64, 0x12, 0x12,
	0x0a, 0x04, 0x66, 0x71, 0x64, 0x6e, 0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x04, 0x66, 0x71,
	0x64, 0x6e, 0x12, 0x12, 0x0a, 0x04, 0x69, 0x70, 0x76, 0x36, 0x18, 0x03, 0x20, 0x01, 0x28, 0x08,
	0x52, 0x04, 0x69, 0x70, 0x76, 0x36, 0x22, 0x3d, 0x0a, 0x0b, 0x44, 0x6e, 0x73, 0x52, 0x65, 0x73,
	0x70, 0x6f, 0x6e, 0x73, 0x65, 0x12, 0x16, 0x0a, 0x06, 0x64, 0x6f, 0x6d, 0x61, 0x69, 0x6e, 0x18,
	0x01, 0x20, 0x01, 0x28, 0x09, 0x52, 0x06, 0x64, 0x6f, 0x6d, 0x61, 0x69, 0x6e, 0x12, 0x16, 0x0a,
	0x06, 0x72, 0x65, 0x73, 0x75, 0x6c, 0x74, 0x18, 0x02, 0x20, 0x01, 0x28, 0x09, 0x52, 0x06, 0x72,
	0x65, 0x73, 0x75, 0x6c, 0x74, 0x2a, 0x3c, 0x0a, 0x0b, 0x50, 0x72, 0x6f, 0x78, 0x79, 0x53, 0x74,
	0x61, 0x74, 0x75, 0x73, 0x12, 0x09, 0x0a, 0x05, 0x45, 0x72, 0x72, 0x6f, 0x72, 0x10, 0x00, 0x12,
	0x0c, 0x0a, 0x08, 0x41, 0x63, 0x63, 0x65, 0x70, 0x74, 0x65, 0x64, 0x10, 0x01, 0x12, 0x0b, 0x0a,
	0x07, 0x53, 0x65, 0x73, 0x73, 0x69, 0x6f, 0x6e, 0x10, 0x03, 0x12, 0x07, 0x0a, 0x03, 0x45, 0x4f,
	0x46, 0x10, 0x04, 0x32, 0x73, 0x0a, 0x05, 0x50, 0x72, 0x6f, 0x78, 0x79, 0x12, 0x39, 0x0a, 0x0a,
	0x44, 0x6e, 0x73, 0x52, 0x65, 0x73, 0x6f, 0x6c, 0x76, 0x65, 0x12, 0x11, 0x2e, 0x70, 0x72, 0x6f,
	0x78, 0x79, 0x2e, 0x44, 0x6e, 0x73, 0x52, 0x65, 0x71, 0x75, 0x65, 0x73, 0x74, 0x1a, 0x12, 0x2e,
	0x70, 0x72, 0x6f, 0x78, 0x79, 0x2e, 0x44, 0x6e, 0x73, 0x52, 0x65, 0x73, 0x70, 0x6f, 0x6e, 0x73,
	0x65, 0x22, 0x00, 0x28, 0x01, 0x30, 0x01, 0x12, 0x2f, 0x0a, 0x05, 0x50, 0x72, 0x6f, 0x78, 0x79,
	0x12, 0x0f, 0x2e, 0x70, 0x72, 0x6f, 0x78, 0x79, 0x2e, 0x50, 0x72, 0x6f, 0x78, 0x79, 0x53, 0x52,
	0x43, 0x1a, 0x0f, 0x2e, 0x70, 0x72, 0x6f, 0x78, 0x79, 0x2e, 0x50, 0x72, 0x6f, 0x78, 0x79, 0x44,
	0x53, 0x54, 0x22, 0x00, 0x28, 0x01, 0x30, 0x01, 0x42, 0x46, 0x5a, 0x44, 0x67, 0x69, 0x74, 0x68,
	0x75, 0x62, 0x2e, 0x63, 0x6f, 0x6d, 0x2f, 0x53, 0x75, 0x7a, 0x75, 0x6b, 0x69, 0x48, 0x6f, 0x6e,
	0x6f, 0x6b, 0x61, 0x2f, 0x73, 0x70, 0x61, 0x63, 0x65, 0x73, 0x68, 0x69, 0x70, 0x2f, 0x69, 0x6e,
	0x74, 0x65, 0x72, 0x6e, 0x61, 0x6c, 0x2f, 0x74, 0x72, 0x61, 0x6e, 0x73, 0x70, 0x6f, 0x72, 0x74,
	0x2f, 0x72, 0x70, 0x63, 0x2f, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x3b, 0x70, 0x72, 0x6f, 0x78, 0x79,
	0x62, 0x06, 0x70, 0x72, 0x6f, 0x74, 0x6f, 0x33,
}

var (
	file_proxy_proto_rawDescOnce sync.Once
	file_proxy_proto_rawDescData = file_proxy_proto_rawDesc
)

func file_proxy_proto_rawDescGZIP() []byte {
	file_proxy_proto_rawDescOnce.Do(func() {
		file_proxy_proto_rawDescData = protoimpl.X.CompressGZIP(file_proxy_proto_rawDescData)
	})
	return file_proxy_proto_rawDescData
}

var file_proxy_proto_enumTypes = make([]protoimpl.EnumInfo, 1)
var file_proxy_proto_msgTypes = make([]protoimpl.MessageInfo, 4)
var file_proxy_proto_goTypes = []any{
	(ProxyStatus)(0),    // 0: proxy.ProxyStatus
	(*ProxySRC)(nil),    // 1: proxy.ProxySRC
	(*ProxyDST)(nil),    // 2: proxy.ProxyDST
	(*DnsRequest)(nil),  // 3: proxy.DnsRequest
	(*DnsResponse)(nil), // 4: proxy.DnsResponse
}
var file_proxy_proto_depIdxs = []int32{
	0, // 0: proxy.ProxyDST.status:type_name -> proxy.ProxyStatus
	3, // 1: proxy.Proxy.DnsResolve:input_type -> proxy.DnsRequest
	1, // 2: proxy.Proxy.Proxy:input_type -> proxy.ProxySRC
	4, // 3: proxy.Proxy.DnsResolve:output_type -> proxy.DnsResponse
	2, // 4: proxy.Proxy.Proxy:output_type -> proxy.ProxyDST
	3, // [3:5] is the sub-list for method output_type
	1, // [1:3] is the sub-list for method input_type
	1, // [1:1] is the sub-list for extension type_name
	1, // [1:1] is the sub-list for extension extendee
	0, // [0:1] is the sub-list for field type_name
}

func init() { file_proxy_proto_init() }
func file_proxy_proto_init() {
	if File_proxy_proto != nil {
		return
	}
	if !protoimpl.UnsafeEnabled {
		file_proxy_proto_msgTypes[0].Exporter = func(v any, i int) any {
			switch v := v.(*ProxySRC); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_proxy_proto_msgTypes[1].Exporter = func(v any, i int) any {
			switch v := v.(*ProxyDST); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_proxy_proto_msgTypes[2].Exporter = func(v any, i int) any {
			switch v := v.(*DnsRequest); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
		file_proxy_proto_msgTypes[3].Exporter = func(v any, i int) any {
			switch v := v.(*DnsResponse); i {
			case 0:
				return &v.state
			case 1:
				return &v.sizeCache
			case 2:
				return &v.unknownFields
			default:
				return nil
			}
		}
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: file_proxy_proto_rawDesc,
			NumEnums:      1,
			NumMessages:   4,
			NumExtensions: 0,
			NumServices:   1,
		},
		GoTypes:           file_proxy_proto_goTypes,
		DependencyIndexes: file_proxy_proto_depIdxs,
		EnumInfos:         file_proxy_proto_enumTypes,
		MessageInfos:      file_proxy_proto_msgTypes,
	}.Build()
	File_proxy_proto = out.File
	file_proxy_proto_rawDesc = nil
	file_proxy_proto_goTypes = nil
	file_proxy_proto_depIdxs = nil
}
