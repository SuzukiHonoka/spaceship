package dns

type Type string

const (
	TypeDefault Type = ""
	TypeCommon  Type = "common" // only supported now
	TypeDOT     Type = "dot"
	TypeDOH     Type = "doh"
)
