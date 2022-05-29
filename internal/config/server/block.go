package server

type BlockType string

const (
	BlockTypeIp       BlockType = "ip"
	BlockTypeIps      BlockType = "ips"
	BlockTypeDomain   BlockType = "domain"
	BlockTypeDomains  BlockType = "domains"
	BlockTypeRegex    BlockType = "regex"
	BlockTypeProtocol BlockType = "protocol" // bittorrent only for now
)

type Block struct {
	BlockType
	Data []string // string slice of domain || domains || regex || protocol
}
